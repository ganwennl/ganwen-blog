import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest

SCRIPT_ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("independent_package", SCRIPT_ROOT / "package-independent-page.py")
packager = importlib.util.module_from_spec(spec)
spec.loader.exec_module(packager)
fixture_spec = importlib.util.spec_from_file_location("independent_fixture", SCRIPT_ROOT / "test_prepare_independent_page.py")
fixture = importlib.util.module_from_spec(fixture_spec)
fixture_spec.loader.exec_module(fixture)


class IndependentPackageTests(unittest.TestCase):
    def fixture(self, root):
        (root / "scripts").mkdir()
        shutil.copyfile(SCRIPT_ROOT / "prepare-independent-page.py", root / "scripts/prepare-independent-page.py")
        (root / "README.md").write_text("Fixture documentation", encoding="utf-8")
        (root / "deploy").mkdir()
        for name in packager.DEPLOY_FILES:
            (root / "deploy" / name).write_text("location / { proxy_pass http://127.0.0.1:8099; }", encoding="utf-8")
        preparer = packager.load_preparer(root)
        prepared = root / "prepared"
        prepared.mkdir()
        raw = fixture.SOURCE
        (prepared / "source.html").write_bytes(raw)
        post, names = preparer.extract_page(raw, "https://www.ganwen.nl/bewijs.html", "/bewijs.html")
        media = []
        for name in names:
            content = ("original " + name).encode("utf-8")
            target = prepared / "media" / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            media.append({"path": name, "bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()})
        package = {"schema": 1, "source_url": "https://www.ganwen.nl/bewijs.html", "post": post,
                   "source": {"path": "source.html", "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}, "media": media}
        (prepared / "page.json").write_text(json.dumps(package, ensure_ascii=False), encoding="utf-8")
        binary = root / "ganwen-linux"
        header = bytearray(20)
        header[:6] = b"\x7fELF\x02\x01"
        header[18:20] = (62).to_bytes(2, "little")
        binary.write_bytes(bytes(header) + b"program-fixture")
        return prepared, binary

    def test_complete_archive_omits_private_runtime_and_verifies_every_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            prepared, binary = self.fixture(root)
            for name in ["runtime.env", "admin-access.txt", "blog.db", "translation.key", "recovery.key"]:
                (prepared / name).write_bytes(b"excluded-private-fixture")
            output = root / "dist/independent.tar.gz"
            result = packager.build_package(prepared, binary, output, root)
            self.assertEqual(result["evidence_files"], 2)
            self.assertEqual(result["verified_files"], 11)
            self.assertEqual(result["sha256"], hashlib.sha256(output.read_bytes()).hexdigest())
            with tarfile.open(output, "r:gz") as archive:
                files = {member.name.removeprefix(packager.ARCHIVE_ROOT + "/"): archive.extractfile(member).read()
                         for member in archive.getmembers() if member.isfile()}
            manifest = json.loads(files["package-manifest.json"])
            self.assertEqual(set(manifest["files"]), set(files) - {"package-manifest.json"})
            for name, entry in manifest["files"].items():
                self.assertEqual(len(files[name]), entry["bytes"])
                self.assertEqual(hashlib.sha256(files[name]).hexdigest(), entry["sha256"])
            self.assertEqual(files["prepared/source.html"], fixture.SOURCE)
            self.assertIn("回滚必须停止项目".encode("utf-8"), files["INSTALL.md"])
            for name, raw in files.items():
                self.assertNotIn("excluded-private-fixture".encode("utf-8"), raw, name)
                self.assertFalse(name.endswith((".key", ".db", "runtime.env", "admin-access.txt")), name)
            before = output.read_bytes()
            with self.assertRaisesRegex(ValueError, "new .tar.gz"):
                packager.build_package(prepared, binary, output, root)
            self.assertEqual(output.read_bytes(), before)

    def test_missing_media_wrong_binary_and_external_output_are_rejected(self):
        for mode in ["missing-evidence", "wrong-binary", "outside-dist"]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                prepared, binary = self.fixture(root)
                output = root / "dist/independent.tar.gz"
                if mode == "missing-evidence":
                    (prepared / "media/proof.jpg").unlink()
                elif mode == "wrong-binary":
                    binary.write_bytes(b"MZ-windows-program-fixture")
                else:
                    output = root / "outside.tar.gz"
                with self.assertRaises((ValueError, FileNotFoundError)):
                    packager.build_package(prepared, binary, output, root)
                self.assertFalse(output.exists())

    def test_unexpected_private_metadata_is_not_carried_into_delivery(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            prepared, binary = self.fixture(root)
            page = prepared / "page.json"
            package = json.loads(page.read_text(encoding="utf-8"))
            package["private_config"] = {"token": "excluded-private-fixture"}
            page.write_text(json.dumps(package), encoding="utf-8")
            output = root / "dist/independent.tar.gz"
            with self.assertRaisesRegex(ValueError, "Unexpected independent-page package metadata"):
                packager.build_package(prepared, binary, output, root)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
