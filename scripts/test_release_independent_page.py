import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("independent_release", ROOT / "scripts/release-independent-page.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)

ORIGINAL_NGINX = """server {
    listen 443 ssl;
    server_name ganwen.nl www.ganwen.nl;
    root /www/wwwroot/ganwen.nl;
    include /www/server/panel/vhost/nginx/extension/ganwen_blog/*.conf;
    ssl_certificate /fixture/fullchain.pem;
    ssl_certificate_key /fixture/key.pem;
    access_log syslog:server=unix:/tmp/bt-monitor.sock,nohostname,tag=1__access monitor;
    proxy_intercept_errors off;
    # The independent evidence page stays exactly in the old site directory.
    location = /bewijs.html {
        try_files $uri =404;
    }

    # Explicit legacy entry remains unchanged.
    location = /index.php { proxy_pass http://127.0.0.1:8099; }
    location ~* \\.(%s)$ {
        try_files $uri @go_media;
        expires 30d;
    }
    location @go_media {
        proxy_pass http://127.0.0.1:8099;
    }
    location / { proxy_pass http://127.0.0.1:8099; }
}
""" % "|".join(sorted(release.MEDIA_EXTENSIONS))


def make_fixture(root):
    project, work = root / "production", root / "maintenance"
    (project / "data/media").mkdir(parents=True)
    work.mkdir()
    binary = project / "ganwen"
    binary.write_bytes(b"original-program-fixture")
    os.chmod(binary, 0o755)
    (project / "data/runtime.env").write_text("GBLOG_DATABASE_DRIVER=sqlite\nGBLOG_DATABASE_PATH=blog.db\n", encoding="utf-8")
    (project / "data/media/unchanged.jpg").write_bytes(b"existing-blog-image")
    connection = sqlite3.connect(project / "data/blog.db")
    try:
        connection.executescript("CREATE TABLE posts(id INTEGER PRIMARY KEY,kind TEXT,status TEXT,published_at TEXT,document TEXT); CREATE TABLE kv(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY,post_id INTEGER);")
        connection.execute("INSERT INTO posts VALUES(1,'post','publish','2026-01-01',?)", ('{"id":1,"title":"Existing blog article"}',))
        connection.execute("INSERT INTO kv VALUES('site',?)", ('{"title":"Existing site"}',))
        connection.commit()
    finally:
        connection.close()
    nginx = root / "nginx.conf"
    nginx.write_text(ORIGINAL_NGINX, encoding="utf-8")
    incoming = root / "package" / release.ARCHIVE_ROOT
    (incoming / "prepared/media").mkdir(parents=True)
    source = "<!doctype html><html><body>original evidence fixture</body></html>".encode("utf-8")
    (incoming / "prepared/source.html").write_bytes(source)
    post = {"type": "page", "status": "publish", "public_path": "/bewijs.html", "unlisted": True,
            "title": "Evidence fixture", "content_html": "<p>Original evidence</p>", "source_html": source.decode("utf-8"),
            "translations": {"nl": {"title": "Bewijs fixture", "content_html": "<p>Origineel bewijs</p>", "status": "publish"}}}
    evidence = []
    for index in range(24):
        relative = "fixture-%02d.jpg" % index if index < 22 else "wp-content/uploads/2025/01/fixture-%02d.jpg" % index
        data = ("original evidence %d" % index).encode("utf-8")
        target = incoming / "prepared/media" / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        evidence.append({"path": relative, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
    page = {"schema": 1, "source_url": "https://www.ganwen.nl/bewijs.html", "post": post,
            "source": {"path": "source.html", "bytes": len(source), "sha256": hashlib.sha256(source).hexdigest()}, "media": evidence}
    (incoming / "prepared/page.json").write_text(json.dumps(page, ensure_ascii=False), encoding="utf-8")
    header = bytearray(20)
    header[:6] = b"\x7fELF\x02\x01"
    header[18:20] = (62).to_bytes(2, "little")
    (incoming / "ganwen").write_bytes(bytes(header) + b"updated-program-fixture")
    (incoming / "README.md").write_text("Fixture readme", encoding="utf-8")
    (incoming / "INSTALL.md").write_text("Fixture installation", encoding="utf-8")
    (incoming / "deploy").mkdir()
    for name in ["ganwen-go.bt.conf.example", "ganwen.nl.bt.conf.example", "nginx-locations.conf.example"]:
        (incoming / "deploy" / name).write_text("Fixture deployment template", encoding="utf-8")
    files = {str(path.relative_to(incoming)).replace("\\", "/"): {"bytes": path.stat().st_size, "sha256": release.digest(path)} for path in incoming.rglob("*") if path.is_file()}
    manifest = {"schema": 1, "files": files, "evidence_files": 24, "evidence_bytes": sum(entry["bytes"] for entry in evidence)}
    (incoming / "package-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    archive = work / "incoming.tar.gz"
    with tarfile.open(archive, "x:gz") as output:
        for path in [incoming] + sorted(incoming.rglob("*")):
            output.add(path, arcname=str(path.relative_to(incoming.parent)).replace("\\", "/"), recursive=False)
    args = SimpleNamespace(work_dir=str(work), project_dir=str(project), nginx_config=str(nginx), nginx_bin="nginx-fixture",
                           archive_name=archive.name, package_sha256=release.digest(archive), expected_old_binary_sha256=release.digest(binary), run_user="www")
    return args, project, work, nginx


class ReleasePreparationTests(unittest.TestCase):
    def test_nginx_patch_preserves_monitor_certificate_and_other_routes(self):
        patched = release.patch_nginx(ORIGINAL_NGINX)
        self.assertNotIn("location = /bewijs.html", patched)
        self.assertIn("error_page 404 = @legacy_media;", patched)
        self.assertIn("proxy_intercept_errors on;", patched)
        self.assertNotIn("location @go_media", patched)
        for line in ORIGINAL_NGINX.splitlines():
            if any(marker in line for marker in ["include ", "ssl_", "listen ", "server_name ", "access_log ", "location = /index.php"]):
                self.assertIn(line, patched)
        self.assertEqual(release.patch_nginx(patched), patched)

    def test_unreviewed_media_changes_refuse_patch(self):
        for changed in [ORIGINAL_NGINX.replace("try_files $uri @go_media;", "try_files $uri @another;"),
                        ORIGINAL_NGINX.replace("location @go_media {", "location @another {"),
                        ORIGINAL_NGINX.replace("try_files $uri =404;\n    }", "return 302 /other;\n    }")]:
            with self.assertRaises(release.ReleaseError):
                release.patch_nginx(changed)

    def test_sqlite_snapshot_keeps_wal_committed_data_and_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            database, snapshot = root / "blog.db", root / "snapshot.sqlite"
            connection = sqlite3.connect(database)
            try:
                connection.execute("PRAGMA journal_mode=WAL")
                connection.executescript("CREATE TABLE posts(id INTEGER PRIMARY KEY, document TEXT); CREATE TABLE kv(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY,post_id INTEGER);")
                connection.execute("INSERT INTO posts VALUES(1,?)", ('{"title":"原文"}',))
                connection.execute("INSERT INTO kv VALUES('site',?)", ('{"title":"站点"}',))
                connection.commit()
                before = release.db_contract(database)
                release.db_snapshot(database, snapshot)
                self.assertEqual(release.db_contract(snapshot), before)
                connection.execute("INSERT INTO posts VALUES(2,?)", ('{"title":"后续内容"}',))
                connection.commit()
                self.assertNotEqual(release.db_contract(database), release.db_contract(snapshot))
                self.assertEqual(len(release.db_contract(snapshot)["posts"]), 1)
            finally:
                connection.close()

    def install_case(self, fail_nginx=False):
        with tempfile.TemporaryDirectory() as temporary:
            args, project, work, nginx = make_fixture(Path(temporary))
            old_program, old_config = release.digest(project / "ganwen"), release.digest(nginx)
            old_runtime = (project / "data/runtime.env").read_bytes()
            before = release.db_contract(project / "data/blog.db")
            calls, permissions = [], []

            def set_permissions(path, attributes):
                os.chmod(path, attributes["mode"])
                permissions.append((Path(path), dict(attributes)))

            def command(arguments):
                calls.append([str(value) for value in arguments])
                if str(arguments[1]) == "import-independent-page":
                    page_path = Path(arguments[arguments.index("--file") + 1])
                    page = json.loads(page_path.read_text(encoding="utf-8"))
                    for entry in page["media"]:
                        target = project / "data/media" / entry["path"]
                        target.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copyfile(page_path.parent / "media" / entry["path"], target)
                    connection = sqlite3.connect(project / "data/blog.db")
                    try:
                        post = {**page["post"], "id": 10000, "version": 1}
                        connection.execute("INSERT INTO posts VALUES(10000,'page','publish','2026-10-13',?)", (json.dumps(post),))
                        connection.execute("INSERT INTO kv VALUES('posts_revision_v1','updated')")
                        connection.commit()
                    finally:
                        connection.close()
                    if fail_nginx:
                        with (project / "data/runtime.env").open("a", encoding="utf-8") as stream:
                            stream.write("# failed-install fixture change\n")
                    return b"Independent page verified"
                if str(arguments[1]) == "-t":
                    if fail_nginx and sum(call[1] == "-t" for call in calls) == 1:
                        raise release.ReleaseError("fixture_nginx_failed")
                    return b""
                raise AssertionError("Unexpected subprocess")

            with patch.object(release, "media_owner", return_value={"uid": 33, "gid": 33}), patch.object(release, "set_metadata", side_effect=set_permissions), patch.object(release, "assert_stopped"), patch.object(release, "run_command", side_effect=command):
                self.assertEqual(release.stage(args)["stage"], "staged")
                self.assertEqual(release.digest(project / "ganwen"), old_program)
                self.assertEqual(release.db_contract(project / "data/blog.db"), before)
                if fail_nginx:
                    with self.assertRaisesRegex(release.ReleaseError, "install_failed_original_restored"):
                        release.install(args)
                    self.assertEqual(release.digest(project / "ganwen"), old_program)
                    self.assertEqual(release.digest(nginx), old_config)
                    self.assertEqual(release.db_contract(project / "data/blog.db"), before)
                    self.assertEqual((project / "data/runtime.env").read_bytes(), old_runtime)
                    self.assertEqual(release.load_state(work)["stage"], "rolled_back")
                    self.assertTrue(list(work.glob("rollback-current-*")))
                else:
                    self.assertEqual(release.install(args)["stage"], "installed")
                    self.assertEqual(len(release.db_contract(project / "data/blog.db")["posts"]), len(before["posts"]) + 1)
                    self.assertTrue(release.install(args)["already_installed"])
                    self.assertEqual(sum(call[1] == "import-independent-page" for call in calls), 1)
                    self.assertEqual(release.rollback(args)["stage"], "rolled_back")
                    self.assertEqual(release.digest(project / "ganwen"), old_program)
                    self.assertEqual(release.digest(nginx), old_config)
                    self.assertEqual(release.db_contract(project / "data/blog.db"), before)
            self.assertEqual((project / "data/media/unchanged.jpg").read_bytes(), b"existing-blog-image")
            new_files = [path for path, attributes in permissions if path.parent == project / "data/media" and path.name.startswith("fixture-") and attributes == {"uid": 33, "gid": 33, "mode": 0o600}]
            self.assertEqual(len(set(new_files)), 22, "new root evidence did not receive www read permissions")
            self.assertTrue(any(path == project / "data/media/wp-content" and attributes == {"uid": 33, "gid": 33, "mode": 0o700} for path, attributes in permissions))

    def test_install_is_idempotent_preserves_data_and_can_roll_back(self):
        self.install_case()

    def test_failed_nginx_test_restores_original_database_program_and_config(self):
        self.install_case(fail_nginx=True)

    def test_changed_program_or_reviewed_candidate_is_refused_before_install(self):
        for mode in ["program", "candidate"]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                args, project, work, nginx = make_fixture(Path(temporary))
                before = release.db_contract(project / "data/blog.db")
                with patch.object(release, "media_owner", return_value={"uid": 33, "gid": 33}), patch.object(release, "assert_stopped"), patch.object(release, "run_command", side_effect=AssertionError("Refusal must not invoke a subprocess")):
                    release.stage(args)
                    target = project / "ganwen" if mode == "program" else work / "nginx.next.conf"
                    target.write_bytes(b"modified after staging")
                    with self.assertRaises(release.ReleaseError):
                        release.install(args)
                self.assertEqual(release.db_contract(project / "data/blog.db"), before)
                self.assertFalse((work / "before-install").exists())

    def test_bundle_checks_real_incremental_archive_when_available(self):
        archive = ROOT / "dist/ganwenblog-independent-page-sidebar-20261013.tar.gz"
        if not archive.is_file():
            self.skipTest("Private release archive is unavailable")
        self.assertEqual(release.digest(archive), release.EXPECTED_PACKAGE)
        with tempfile.TemporaryDirectory() as temporary:
            staging = Path(temporary) / "stage"
            release.extract_bundle(archive, staging)
            prepared, manifest = release.verify_bundle(staging / release.ARCHIVE_ROOT)
            self.assertEqual(len(prepared["media"]), 24)
            self.assertEqual(len(manifest["files"]), 32)


if __name__ == "__main__":
    unittest.main()
