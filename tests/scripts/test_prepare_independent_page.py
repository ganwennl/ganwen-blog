import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("independent_prepare", (Path(__file__).resolve().parents[2] / "scripts/migration/prepare-independent-page.py"))
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)

SOURCE = '''<!doctype html><html><head><script src="https://cdn.invalid/asset.js"></script></head><body>
<header id="header-zh"><h1>证据原题</h1><h2>Original evidence</h2></header>
<header id="header-nl"><h1>Origineel bewijs</h1><h2>Original evidence</h2></header>
<div id="lang-zh" class="hidden"><h3 id="A1">证据 A1</h3><p>原文 &amp; 记录 <a href="#A1">编号</a></p>
<img src="/proof.jpg" alt="原始证据"><video controls><source src="https://ganwen.nl/wp-content/uploads/clip.mp4" type="video/mp4"></video>
<a href="https://news.example/article">引用</a><button onclick="window.print()">打印</button></div>
<div id="lang-nl"><h3 id="A1">Bewijs A1</h3><p>Origineel &amp; record <a href="#A1">Nummer</a></p>
<img src="https://www.ganwen.nl/proof.jpg" alt="Origineel"><video controls><source src="/wp-content/uploads/clip.mp4" type="video/mp4"></video></div>
</body></html>'''.encode("utf-8")


class IndependentPreparationTests(unittest.TestCase):
    def test_preserves_two_originals_anchors_and_complete_media(self):
        post, paths = prepare.extract_page(SOURCE, "https://www.ganwen.nl/bewijs.html", "/bewijs.html")
        self.assertEqual(paths, ["proof.jpg", "wp-content/uploads/clip.mp4"])
        self.assertEqual(post["source_html"].encode("utf-8"), SOURCE)
        self.assertTrue(post["unlisted"])
        self.assertEqual(post["public_path"], "/bewijs.html")
        self.assertIn('id="A1"', post["content_html"])
        self.assertIn('href="#A1"', post["translations"]["nl"]["content_html"])
        self.assertIn('href="https://news.example/article"', post["content_html"])
        self.assertNotIn("hidden", post["content_html"])
        self.assertNotIn("button", post["content_html"])
        self.assertNotIn("script", post["content_html"])
        self.assertEqual(post["translations"]["nl"]["source_hash"], prepare.source_hash(post))
        self.assertEqual(post["translations"]["nl"]["status"], "publish")

    def test_complete_package_is_idempotent_and_refuses_changed_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "original.html"
            source.write_bytes(SOURCE)
            media_root = root / "existing-media"
            files = {"proof.jpg": b"jpeg-fixture", "wp-content/uploads/clip.mp4": b"video-fixture"}
            manifest = []
            for relative, raw in files.items():
                target = media_root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(raw)
                manifest.append({"path": relative, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()})
            manifest_path = root / "original-manifest.json"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            destination = root / "prepared"
            with patch.object(prepare, "read_https", side_effect=AssertionError("No network required")):
                result = prepare.prepare(destination, "https://www.ganwen.nl/bewijs.html", "/bewijs.html", source, media_root, manifest_path)
                self.assertTrue(result["prepared"])
                package_bytes = (destination / "page.json").read_bytes()
                result = prepare.prepare(destination, "https://www.ganwen.nl/bewijs.html", "/bewijs.html", source, media_root, manifest_path)
                self.assertFalse(result["prepared"])
                self.assertEqual((destination / "page.json").read_bytes(), package_bytes)
                (destination / "media/proof.jpg").write_bytes(b"changed")
                with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                    prepare.prepare(destination, "https://www.ganwen.nl/bewijs.html", "/bewijs.html", source, media_root, manifest_path)

    def test_missing_language_and_unsafe_paths_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "header and body"):
            prepare.extract_page(SOURCE.replace(b'id="lang-nl"', b'id="missing"'), "https://www.ganwen.nl/bewijs.html", "/bewijs.html")
        for path in ("/../secret.jpg", "/%2e%2e/secret.jpg", "/dir\\secret.jpg", "/C:/secret.jpg"):
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "Unsafe media path"):
                prepare.safe_relative_path(path)

    def test_failed_evidence_preparation_does_not_publish_package(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "original.html"
            source.write_bytes(SOURCE)
            destination = root / "prepared"
            with patch.object(prepare, "read_https", side_effect=ValueError("Evidence unavailable")):
                with self.assertRaisesRegex(ValueError, "Evidence unavailable"):
                    prepare.prepare(destination, "https://www.ganwen.nl/bewijs.html", "/bewijs.html", source)
            self.assertFalse(destination.exists())
            incoming = list(root.glob("prepared.preparing-*"))
            self.assertEqual(len(incoming), 1)
            self.assertEqual((incoming[0] / "source.html").read_bytes(), SOURCE)


if __name__ == "__main__":
    unittest.main()
