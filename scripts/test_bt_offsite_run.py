"""Offline backup contracts: fake Drive, fake crypto, and isolated local files."""
import contextlib
import base64
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import ssl
import struct
import sys
import tempfile
import types
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("bt-offsite-run.py")
fcntl_stub = types.ModuleType("fcntl")
fcntl_stub.LOCK_EX, fcntl_stub.LOCK_NB = 2, 4
fcntl_stub.flock = lambda *_: None
with mock.patch.dict(sys.modules, {"fcntl": fcntl_stub}):
    spec = importlib.util.spec_from_file_location("bt_offsite_test_subject", SCRIPT)
    backup = importlib.util.module_from_spec(spec)
    previous_bytecode = sys.dont_write_bytecode
    sys.dont_write_bytecode = True
    try:
        spec.loader.exec_module(backup)
    finally:
        sys.dont_write_bytecode = previous_bytecode


def private_folder():
    return {"id": "private-folder", "mimeType": "application/vnd.google-apps.folder",
            "trashed": False, "shared": False,
            "owners": [{"emailAddress": "ganwen33@gmail.com"}],
            "capabilities": {"canAddChildren": True}}


class Request:
    def __init__(self, result):
        self.result = result

    def execute(self, **_):
        return copy.deepcopy(self.result() if callable(self.result) else self.result)


class Media:
    def __init__(self, path, **options):
        self.path, self.options = path, options


class HttpFailure(Exception):
    def __init__(self, status, reason="rateLimitExceeded", message="FAKE_SECRET_MUST_NOT_BE_LOGGED"):
        super().__init__("FAKE_SECRET_MUST_NOT_BE_LOGGED")
        self.resp = types.SimpleNamespace(status=status)
        self.content = json.dumps({"error": {"errors": [{"reason": reason}],
            "message": message}}).encode("utf-8")


def google_modules(client_factory=None, builder=None):
    package = types.ModuleType("googleapiclient")
    package.__path__ = []
    http = types.ModuleType("googleapiclient.http")
    http.MediaFileUpload = Media
    discovery = types.ModuleType("googleapiclient.discovery")
    discovery.build = builder or mock.Mock()
    plugin = types.ModuleType("gdrive_main")
    plugin.gdrive_main = client_factory or mock.Mock()
    return {"googleapiclient": package, "googleapiclient.http": http,
            "googleapiclient.discovery": discovery, "gdrive_main": plugin}


def public_key_modules(key_size=3072):
    """Envelope validation uses an isolated RSA public-key interface, no secrets."""
    class PublicKey:
        def __init__(self):
            self.key_size = key_size
        def public_numbers(self):
            return types.SimpleNamespace(n=2 ** key_size - 1)
    names = ["cryptography", "cryptography.hazmat", "cryptography.hazmat.primitives",
             "cryptography.hazmat.primitives.asymmetric"]
    modules = {name: types.ModuleType(name) for name in names}
    for module in modules.values():
        module.__path__ = []
    serialization = types.ModuleType("cryptography.hazmat.primitives.serialization")
    serialization.load_pem_public_key = mock.Mock(return_value=PublicKey())
    rsa = types.ModuleType("cryptography.hazmat.primitives.asymmetric.rsa")
    rsa.RSAPublicKey = PublicKey
    modules[serialization.__name__], modules[rsa.__name__] = serialization, rsa
    return modules


class UploadRequest:
    def __init__(self, drive, file_id):
        self.drive, self.file_id, self.calls = drive, file_id, 0

    def next_chunk(self, **_):
        self.calls += 1
        if self.drive.upload_error:
            raise self.drive.upload_error
        if self.calls == 1:
            return types.SimpleNamespace(progress=lambda: 0.30), None
        return None, {} if self.drive.empty_id else {"id": self.file_id}


class FakeDrive:
    def __init__(self):
        self.metadata = {"private-folder": private_folder()}
        self.overrides, self.created, self.updated, self.queries = {}, [], [], []
        self.pages = [{"files": []}]
        self.upload_error, self.empty_id, self.trash_verified = None, False, True

    def files(self):
        return self

    def get(self, fileId, **_):
        return Request(lambda: dict(self.metadata[fileId], **self.overrides))

    def create(self, body, media_body, **_):
        content = Path(media_body.path).read_bytes()
        file_id = "uploaded-{}".format(len(self.created) + 1)
        self.created.append((copy.deepcopy(body), media_body))
        self.metadata[file_id] = dict(body, id=file_id, size=str(len(content)),
            md5Checksum=hashlib.md5(content).hexdigest(), trashed=False, shared=False)
        return UploadRequest(self, file_id)

    def list(self, pageToken=None, **options):
        self.queries.append(dict(options, pageToken=pageToken))
        return Request(self.pages[int(pageToken or "0")])

    def update(self, fileId, body, **_):
        self.updated.append((fileId, body))
        return Request({"id": fileId, "trashed": self.trash_verified})

    def delete(self, **_):
        raise AssertionError("Permanent remote deletion is forbidden")


def remote_entry(index, **changes):
    result = {"id": "remote-{:06d}".format(index),
              "name": backup.PREFIX + "{:06d}.tar.gz.enc".format(index),
              "createdTime": "2026-01-01T{:06d}Z".format(index),
              "parents": ["private-folder"],
              "appProperties": {"backupOwner": backup.OWNER}}
    result.update(changes)
    return result


def native_fixture():
    class NativeDrive(FakeDrive):
        def list(self, q, **options):
            parent = q.split("'")[1]
            self.queries.append(dict(options, q=q))
            if parent not in self.items:
                raise AssertionError("Unexpected native folder outside the fixture")
            return Request({"files": self.items[parent]})

    drive = NativeDrive()
    for folder in [backup.NATIVE_WEB_PARENT, backup.NATIVE_DATABASE_PARENT, "native-site", "native-sql"]:
        drive.metadata[folder] = dict(private_folder(), id=folder)
    drive.items = {
        backup.NATIVE_WEB_PARENT: [
            {"id": "native-site", "name": "registered.nl", "parents": [backup.NATIVE_WEB_PARENT]},
            {"id": "unregistered-site", "name": "gone.nl", "parents": [backup.NATIVE_WEB_PARENT]}],
        backup.NATIVE_DATABASE_PARENT: [
            {"id": "native-sql", "name": "registered_db", "parents": [backup.NATIVE_DATABASE_PARENT]},
            {"id": "remote-sql", "name": "remote_db", "parents": [backup.NATIVE_DATABASE_PARENT]}],
        "native-site": [
            {"id": "web-old", "name": "web_registered.nl_20261001_000000_a1.tar.gz", "parents": ["native-site"], "createdTime": "2026-10-01T00:00:00Z"},
            {"id": "web-new", "name": "web_registered.nl_20261002_000000_a2.tar.gz", "parents": ["native-site"], "createdTime": "2026-10-02T00:00:00Z"},
            {"id": "wrong-project", "name": "web_someone.nl_20261001_000000_a1.tar.gz", "parents": ["native-site"], "createdTime": "2026-09-01T00:00:00Z"},
            {"id": "wrong-format", "name": "web_registered.nl_manual.tar.gz", "parents": ["native-site"], "createdTime": "2026-09-01T00:00:00Z"},
            {"id": "wrong-parent", "name": "web_registered.nl_20261001_000000_a1.tar.gz", "parents": ["someone-else"], "createdTime": "2026-09-01T00:00:00Z"}],
        "native-sql": [
            {"id": "sql-old", "name": "registered_db_2026-10-01_00-00-00_mysql_data.sql.gz", "parents": ["native-sql"], "createdTime": "2026-10-01T00:00:00Z"},
            {"id": "sql-new", "name": "registered_db_2026-10-02_00-00-00_mysql_data.sql.gz", "parents": ["native-sql"], "createdTime": "2026-10-02T00:00:00Z"},
            {"id": "sql-unrelated", "name": "other_db_2026-10-01_00-00-00_mysql_data.sql.gz", "parents": ["native-sql"], "createdTime": "2026-09-01T00:00:00Z"}]}
    rows = {"sites": [{"name": "registered.nl"}], "databases": [
        {"name": "registered_db", "type": "MySQL", "sid": 0},
        {"name": "remote_db", "type": "mysql", "sid": 3},
        {"name": "postgres_db", "type": "pgsql", "sid": 0}]}
    class Table:
        def __init__(self, name):
            self.name = name
        def field(self, _):
            return self
        def select(self):
            return copy.deepcopy(rows[self.name])
    public = types.ModuleType("public")
    public.M = Table
    return drive, public


class BackupContracts(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="bt-offsite-offline-test-")
        # Windows TEMP can use an 8.3 user-name alias; resolve it before the
        # production Linux symlink-path guard evaluates the isolated fixture.
        self.root = Path(self.temporary.name).resolve()
        self.addCleanup(self.temporary.cleanup)

    def ciphertext(self):
        path = self.root / (backup.PREFIX + "fixture.tar.gz.enc")
        path.write_bytes(b"isolated fake encrypted backup\x00" * 50)
        return path

    def local_backup(self, directory, index):
        directory.mkdir(parents=True, exist_ok=True)
        name = backup.PREFIX + "{:012d}.tar.gz.enc".format(index)
        path = directory / name
        path.write_bytes(b"fake ciphertext " + str(index).encode("ascii"))
        record = {"name": name, "id": "remote-{}".format(index),
                  "parent": "private-folder", "backup_owner": backup.OWNER,
                  "completed_utc": "2026-01-{:02d}T00:00:00Z".format(index + 1),
                  "size": path.stat().st_size, "sha256": backup.digest(path),
                  "md5": backup.digest(path, "md5")}
        receipt = directory / (name + ".json")
        receipt.write_text(json.dumps(record), encoding="utf-8")
        return path, receipt, record

    def test_auth_reuses_existing_object_and_suppresses_plugin_output(self):
        credential = object()

        class Client:
            def __init__(self):
                print("FAKE_CREDENTIAL_DIAGNOSTIC")
                self._gdrive_main__creds = credential

            def set_creds(self):
                print("FAKE_CREDENTIAL_DIAGNOSTIC", file=sys.stderr)
                return True

        builder, old_cwd, old_path = mock.Mock(return_value="authenticated-service"), Path.cwd(), sys.path[:]
        output, errors = io.StringIO(), io.StringIO()
        try:
            with mock.patch.dict(sys.modules, google_modules(Client, builder)), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                self.assertEqual(backup.drive_service(self.root), "authenticated-service")
        finally:
            backup.os.chdir(str(old_cwd))
            sys.path[:] = old_path
        builder.assert_called_once_with("drive", "v3", credentials=credential, cache_discovery=False)
        self.assertNotIn("scopes", builder.call_args[1])
        self.assertNotIn("FAKE_CREDENTIAL_DIAGNOSTIC", output.getvalue() + errors.getvalue())

    def test_unavailable_auth_never_builds_service(self):
        builder = mock.Mock()
        client = types.SimpleNamespace(set_creds=lambda: False)
        old_cwd, old_path = Path.cwd(), sys.path[:]
        try:
            with mock.patch.dict(sys.modules, google_modules(lambda: client, builder)):
                with self.assertRaises(RuntimeError):
                    backup.drive_service(self.root)
        finally:
            backup.os.chdir(str(old_cwd))
            sys.path[:] = old_path
        builder.assert_not_called()

    def test_folder_requires_only_intended_owner_and_explicit_privacy(self):
        drive = FakeDrive()
        self.assertEqual(backup.verify_parent(drive, "private-folder"), "private-folder")
        for change in [{"owners": []}, {"owners": [{"emailAddress": "someone@example.invalid"}]},
                       {"owners": [{"emailAddress": "ganwen33@gmail.com"}, {"emailAddress": "someone@example.invalid"}]},
                       {"shared": True}, {"shared": None}, {"trashed": True},
                       {"capabilities": {"canAddChildren": False}}, {"mimeType": "application/octet-stream"}]:
            with self.subTest(change=change):
                drive.metadata["private-folder"] = dict(private_folder(), **change)
                with self.assertRaises(RuntimeError):
                    backup.verify_parent(drive, "private-folder")

    def test_upload_receipt_requires_readback_checksum_size_parent_marker_privacy(self):
        path = self.ciphertext()
        with mock.patch.dict(sys.modules, google_modules()), contextlib.redirect_stdout(io.StringIO()):
            drive = FakeDrive()
            receipt = backup.upload_verified(drive, path, "private-folder")
            self.assertEqual(receipt["size"], path.stat().st_size)
            self.assertEqual(receipt["sha256"], backup.digest(path))
            self.assertEqual(receipt["md5"], backup.digest(path, "md5"))
            self.assertEqual(receipt["parent"], "private-folder")
            self.assertEqual(drive.created[0][0]["appProperties"], {"backupOwner": backup.OWNER})
            self.assertTrue(drive.created[0][1].options["resumable"])
            for change in [{"size": "0"}, {"md5Checksum": "0" * 32}, {"parents": ["other-folder"]},
                           {"parents": ["private-folder", "extra-folder"]}, {"appProperties": {}},
                           {"appProperties": {"backupOwner": "another-script"}},
                           {"shared": True}, {"shared": None}, {"trashed": True}]:
                with self.subTest(change=change):
                    drive = FakeDrive()
                    drive.overrides = change
                    with self.assertRaises(RuntimeError):
                        backup.upload_verified(drive, path, "private-folder")
            drive = FakeDrive()
            drive.empty_id = True
            with self.assertRaises(RuntimeError):
                backup.upload_verified(drive, path, "private-folder")

    def test_interrupted_upload_keeps_local_ciphertext(self):
        path = self.ciphertext()
        original = path.read_bytes()
        drive = FakeDrive()
        drive.upload_error = OSError("isolated simulated interruption")
        with mock.patch.dict(sys.modules, google_modules()), self.assertRaises(OSError):
            backup.upload_verified(drive, path, "private-folder")
        self.assertEqual(path.read_bytes(), original)

    def test_rate_limited_upload_retries_same_resumable_request_and_redacts_body(self):
        path, drive, output = self.ciphertext(), FakeDrive(), io.StringIO()
        original_chunk, failures = UploadRequest.next_chunk, [HttpFailure(403), HttpFailure(429)]
        def limited_chunk(request, **options):
            self.assertEqual(options["num_retries"], 0)
            if failures:
                raise failures.pop(0)
            return original_chunk(request, **options)
        with mock.patch.dict(sys.modules, google_modules()), mock.patch.object(UploadRequest, "next_chunk", limited_chunk), mock.patch.object(backup.time, "sleep") as sleep, mock.patch.object(backup.random, "uniform", return_value=0), contextlib.redirect_stdout(output):
            receipt = backup.upload_verified(drive, path, "private-folder")
        self.assertEqual(receipt["id"], "uploaded-1")
        self.assertEqual(len(drive.created), 1)
        self.assertEqual(sleep.call_args_list, [mock.call(1), mock.call(2)])
        self.assertEqual(output.getvalue().count("DRIVE_RETRY"), 2)
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_upload_rate_limit_retries_are_bounded_and_preserve_ciphertext(self):
        path, drive = self.ciphertext(), FakeDrive()
        drive.upload_error = HttpFailure(429)
        with mock.patch.dict(sys.modules, google_modules()), mock.patch.object(backup.time, "sleep") as sleep, mock.patch.object(backup.random, "uniform", return_value=0), contextlib.redirect_stdout(io.StringIO()), self.assertRaises(HttpFailure):
            backup.upload_verified(drive, path, "private-folder")
        self.assertEqual(sleep.call_count, backup.DRIVE_ATTEMPTS - 1)
        self.assertEqual(sleep.call_args_list, [mock.call(delay) for delay in [1, 2, 4, 8, 16, 32]])
        self.assertGreaterEqual(sum(call[0][0] for call in sleep.call_args_list), 60)
        self.assertEqual(len(drive.created), 1)
        self.assertTrue(path.exists())

    def test_per_minute_quota_recovers_after_full_window_backoff(self):
        request = mock.Mock()
        request.execute.side_effect = [HttpFailure(403, "quotaExceeded", "Requests per minute") for _ in range(6)] + [{"id": "verified"}]
        with mock.patch.object(backup.time, "sleep") as sleep, mock.patch.object(backup.random, "uniform", return_value=0), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(backup.drive_request(request, "verify_folder"), {"id": "verified"})
        self.assertEqual(request.execute.call_count, 7)
        self.assertEqual(sum(call[0][0] for call in sleep.call_args_list), 63)

    def test_transient_tls_upload_retries_same_session_without_relaxing_tls(self):
        path, drive = self.ciphertext(), FakeDrive()
        original_chunk, failures = UploadRequest.next_chunk, [ssl.SSLEOFError(8, "FAKE_SECRET_MUST_NOT_BE_LOGGED")]
        def interrupted_chunk(request, **options):
            if failures:
                raise failures.pop()
            return original_chunk(request, **options)
        output = io.StringIO()
        with mock.patch.dict(sys.modules, google_modules()), mock.patch.object(UploadRequest, "next_chunk", interrupted_chunk), mock.patch.object(backup.time, "sleep") as sleep, contextlib.redirect_stdout(output):
            self.assertEqual(backup.upload_verified(drive, path, "private-folder")["id"], "uploaded-1")
        sleep.assert_called_once()
        self.assertEqual(len(drive.created), 1)
        self.assertIn("drive_tls_transmission_interrupted", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_tls_certificate_verification_failures_are_not_retried(self):
        for error in [ssl.SSLCertVerificationError(1, "FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                      ssl.SSLError(1, "CERTIFICATE_VERIFY_FAILED FAKE_SECRET_MUST_NOT_BE_LOGGED")]:
            with self.subTest(error_type=type(error).__name__):
                request = mock.Mock()
                request.execute.side_effect = error
                with mock.patch.object(backup.time, "sleep") as sleep, self.assertRaises(ssl.SSLError):
                    backup.drive_request(request, "verify_folder")
                sleep.assert_not_called()
                self.assertEqual(backup.drive_failure(error), ("drive_tls_certificate_verification_failed", False))

    def test_permission_and_daily_quota_errors_are_not_retried(self):
        for status, reason in [(401, "authError"), (403, "insufficientPermissions"), (403, "dailyLimitExceeded")]:
            with self.subTest(status=status, reason=reason):
                request = mock.Mock()
                request.execute.side_effect = HttpFailure(status, reason)
                with mock.patch.object(backup.time, "sleep") as sleep, self.assertRaises(HttpFailure):
                    backup.drive_request(request, "verify_folder")
                sleep.assert_not_called()
                request.execute.assert_called_once_with(num_retries=0)

    def test_explicit_per_minute_quota_exceeded_is_retried_without_raw_message(self):
        for message in ["Quota exceeded: Requests per minute FAKE_SECRET_MUST_NOT_BE_LOGGED",
                        "Quota exceeded for quota metric 'Queries' and limit 'Queries per minute per user' FAKE_SECRET_MUST_NOT_BE_LOGGED"]:
            with self.subTest(message=message):
                request, output = mock.Mock(), io.StringIO()
                request.execute.side_effect = [HttpFailure(403, "quotaExceeded", message), {"id": "verified"}]
                with mock.patch.object(backup.time, "sleep") as sleep, contextlib.redirect_stdout(output):
                    self.assertEqual(backup.drive_request(request, "verify_folder"), {"id": "verified"})
                sleep.assert_called_once()
                self.assertIn("drive_rate_limited", output.getvalue())
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_storage_daily_and_unspecified_quota_exceeded_are_not_retried(self):
        for reason, message in [("storageQuotaExceeded", "Requests per minute"),
                                ("quotaExceeded", "Quota exceeded for Queries per day"),
                                ("quotaExceeded", "Quota exceeded")]:
            with self.subTest(reason=reason, message=message):
                request = mock.Mock()
                request.execute.side_effect = HttpFailure(403, reason, message)
                with mock.patch.object(backup.time, "sleep") as sleep, self.assertRaises(HttpFailure):
                    backup.drive_request(request, "verify_folder")
                sleep.assert_not_called()
                request.execute.assert_called_once_with(num_retries=0)

    def test_remote_retention_paginates_and_trashes_only_oldest_excess(self):
        drive = FakeDrive()
        entries = [remote_entry(index) for index in range(3651)]
        drive.pages = [{"files": entries[:2000], "nextPageToken": "1"}, {"files": entries[2000:]}]
        self.assertEqual(backup.retain_remote(drive, "private-folder", 3650), 3650)
        self.assertEqual(drive.updated, [("remote-000000", {"trashed": True})])
        self.assertEqual([item["pageToken"] for item in drive.queries], [None, "1"])
        self.assertIn("'private-folder' in parents", drive.queries[0]["q"])
        self.assertIn(backup.OWNER, drive.queries[0]["q"])
        drive = FakeDrive()
        drive.pages = [{"files": entries[:3650]}]
        self.assertEqual(backup.retain_remote(drive, "private-folder", 3650), 3650)
        self.assertFalse(drive.updated)

    def test_remote_retention_rejects_wrong_parent_name_or_marker(self):
        for change in [{"parents": ["other-folder"]}, {"name": "someone-else.tar.gz.enc"},
                       {"appProperties": {"backupOwner": "someone-else"}}]:
            with self.subTest(change=change):
                drive = FakeDrive()
                drive.pages = [{"files": [remote_entry(2), remote_entry(1, **change)]}]
                with self.assertRaises(RuntimeError):
                    backup.retain_remote(drive, "private-folder", 1)
                self.assertFalse(drive.updated)

    def test_remote_trash_failure_is_not_success(self):
        drive = FakeDrive()
        drive.pages = [{"files": [remote_entry(1), remote_entry(2)]}]
        drive.trash_verified = False
        with self.assertRaises(RuntimeError):
            backup.retain_remote(drive, "private-folder", 1)

    def test_native_retention_only_trashes_registered_local_matching_backups(self):
        drive, public = native_fixture()
        with mock.patch.dict(sys.modules, {"public": public}):
            counts = backup.retain_native(drive, 1)
        self.assertEqual(counts, {"web/registered.nl": 1, "mysql/registered_db": 1})
        self.assertEqual(drive.updated, [("web-old", {"trashed": True}), ("sql-old", {"trashed": True})])
        self.assertFalse(any("unregistered-site" in item["q"] or "remote-sql" in item["q"] for item in drive.queries))

    def test_ganwen_go_retention_uses_original_folder_with_separate_file_patterns(self):
        drive, public = native_fixture()
        original_table = public.M
        class GanwenTable:
            def field(self, _):
                return self
            def select(self):
                return [{"name": "ganwen.nl"}, {"name": "ganwen_blog"}]
        public.M = lambda name: GanwenTable() if name == "sites" else original_table(name)
        drive.items[backup.NATIVE_WEB_PARENT] = [
            {"id": backup.GANWEN_FOLDER, "name": "ganwen.nl", "parents": [backup.NATIVE_WEB_PARENT]}]
        drive.metadata[backup.GANWEN_FOLDER] = dict(private_folder(), id=backup.GANWEN_FOLDER)
        drive.items[backup.GANWEN_FOLDER] = [
            {"id": name, "name": "web_" + name + "_20261007_210000_a1.tar.gz",
             "parents": [backup.GANWEN_FOLDER], "createdTime": "2026-10-07T21:00:00Z"}
            for name in ["ganwen.nl", "ganwen_blog"]]
        with mock.patch.dict(sys.modules, {"public": public}):
            counts = backup.retain_native(drive, 3650)
        self.assertEqual(counts["web/ganwen.nl"], 1)
        self.assertEqual(counts["web/ganwen_blog"], 1)
        self.assertFalse(drive.updated)

    def test_database_retention_preserves_original_shared_name_website_folder(self):
        drive, public = native_fixture()
        drive.items[backup.NATIVE_DATABASE_PARENT] = []
        drive.items[backup.NATIVE_WEB_PARENT].append(
            {"id": "native-sql", "name": "registered_db", "parents": [backup.NATIVE_WEB_PARENT]})
        with mock.patch.dict(sys.modules, {"public": public}):
            counts = backup.retain_native(drive, 1)
        self.assertEqual(counts["mysql/registered_db"], 1)
        self.assertIn(("sql-old", {"trashed": True}), drive.updated)
        self.assertNotIn(("sql-unrelated", {"trashed": True}), drive.updated)

    def test_native_retention_rejects_duplicate_folder_under_same_parent(self):
        drive, public = native_fixture()
        drive.items[backup.NATIVE_WEB_PARENT].append({"id": "duplicate", "name": "registered.nl", "parents": [backup.NATIVE_WEB_PARENT]})
        with mock.patch.dict(sys.modules, {"public": public}), self.assertRaises(RuntimeError):
            backup.retain_native(drive, 1)
        self.assertFalse(drive.updated)

    def test_native_retention_requires_private_folder_and_verified_trash(self):
        drive, public = native_fixture()
        drive.metadata["native-site"]["shared"] = True
        with mock.patch.dict(sys.modules, {"public": public}), self.assertRaises(RuntimeError):
            backup.retain_native(drive, 1)
        self.assertFalse(drive.updated)
        drive, public = native_fixture()
        drive.trash_verified = False
        with mock.patch.dict(sys.modules, {"public": public}), self.assertRaises(RuntimeError):
            backup.retain_native(drive, 1)

    def test_native_new_project_reports_waiting_without_touching_unregistered_folders(self):
        drive, public = native_fixture()
        drive.items[backup.NATIVE_WEB_PARENT] = [drive.items[backup.NATIVE_WEB_PARENT][1]]
        with mock.patch.dict(sys.modules, {"public": public}):
            counts = backup.retain_native(drive, 3650)
        self.assertEqual(counts["web/registered.nl"], "awaiting_native_run")
        self.assertFalse(drive.updated)

    def test_local_retention_keeps_seven_verified_and_all_unuploaded_files(self):
        directory = self.root / "local"
        entries = [self.local_backup(directory, index) for index in range(9)]
        orphan = directory / (backup.PREFIX + "unuploaded.tar.gz.enc")
        orphan.write_bytes(b"keep unuploaded encrypted data")
        unrelated = directory / "other-project.tar.gz.enc"
        unrelated.write_bytes(b"keep unrelated data")
        backup.retain_local(directory, 7, "private-folder")
        self.assertFalse(entries[0][0].exists())
        self.assertFalse(entries[1][1].exists())
        self.assertTrue(all(path.exists() and receipt.exists() for path, receipt, _ in entries[2:]))
        self.assertTrue(orphan.exists())
        self.assertTrue(unrelated.exists())

    def test_invalid_upload_receipt_cannot_authorize_local_deletion(self):
        for index, change in enumerate([{"id": None}, {"parent": "other-folder"},
                                      {"backup_owner": "other-script"}, {"size": 0},
                                      {"md5": "0" * 32}, {"sha256": "0" * 64},
                                      {"name": "../unrelated.tar.gz.enc"}]):
            with self.subTest(change=change):
                directory = self.root / ("invalid-" + str(index))
                path, receipt, record = self.local_backup(directory, 0)
                another, _, _ = self.local_backup(directory, 1)
                record.update(change)
                receipt.write_text(json.dumps(record), encoding="utf-8")
                with self.assertRaises(RuntimeError):
                    backup.retain_local(directory, 1, "private-folder")
                self.assertTrue(path.exists() and another.exists())

    def test_receipt_filename_must_match_record_and_not_double_count_file(self):
        directory = self.root / "duplicate"
        path, receipt, record = self.local_backup(directory, 0)
        duplicate = directory / (backup.PREFIX + "duplicated.tar.gz.enc.json")
        duplicate.write_text(json.dumps(record), encoding="utf-8")
        with self.assertRaises(RuntimeError):
            backup.retain_local(directory, 1, "private-folder")
        self.assertTrue(path.exists() and receipt.exists())

    @contextlib.contextmanager
    def pipeline(self, drive=None):
        here = self.root / "helpers"
        here.mkdir(exist_ok=True)
        (here / "bt-backup-crypto.py").write_text(
            "from pathlib import Path\ndef process(source, target, key):\n"
            "    Path(target).write_bytes(b'FAKE-ENCRYPTED:' + Path(source).read_bytes())\n", encoding="utf-8")
        directory = self.root / "backup"
        args = types.SimpleNamespace(backup_dir=str(directory), panel=str(self.root),
            folder="private-folder", public_key="unused-fake-public-key", cloud_keep=3650, local_keep=7)
        drive = drive or FakeDrive()
        def pack(command, **options):
            self.assertTrue(options["check"])
            Path(command[command.index("--output") + 1]).write_bytes(b"fake staged archive")
        with mock.patch.dict(sys.modules, google_modules()), mock.patch.object(backup, "__file__", str(here / "bt-offsite-run.py")), mock.patch.object(backup, "drive_service", return_value=drive), mock.patch.object(backup, "retain_native", return_value={}), mock.patch.object(backup.subprocess, "run", side_effect=pack) as packer:
            yield args, directory, drive, packer

    def encrypted_envelope(self, directory, header_changes=None):
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / (backup.PREFIX + "20261013T130532Z-c2ada61ee895.tar.gz.enc")
        header = {"version": 1, "cipher": "AES-256-GCM+RSA-OAEP-SHA256",
                  "key": base64.b64encode(b"1" * 384).decode("ascii"),
                  "nonce": base64.b64encode(b"2" * 12).decode("ascii")}
        header.update(header_changes or {})
        encoded = json.dumps(header).encode("utf-8")
        path.write_bytes(backup.ENCRYPTED_MAGIC + struct.pack(">I", len(encoded)) + encoded + b"payload-ciphertext" + b"tag-not-a-secret")
        return path

    @contextlib.contextmanager
    def retry_pipeline(self, drive=None):
        with self.pipeline(drive) as (args, directory, service, packer):
            public_key = self.root / "fixture-public.pem"
            public_key.write_text("isolated public key fixture", encoding="utf-8")
            args.public_key = str(public_key)
            encrypted = self.encrypted_envelope(directory)
            args.retry_encrypted = str(encrypted)
            with mock.patch.dict(sys.modules, public_key_modules()):
                yield args, directory, service, packer, encrypted

    def existing_remote(self, drive, path, **changes):
        meta = {"id": "already-uploaded", "name": path.name, "size": str(path.stat().st_size),
                "md5Checksum": backup.digest(path, "md5"), "parents": ["private-folder"],
                "trashed": False, "shared": False, "appProperties": {"backupOwner": backup.OWNER},
                "createdTime": "2026-10-13T13:05:32Z"}
        meta.update(changes)
        drive.metadata[meta["id"]] = meta
        drive.pages = [{"files": [meta]}]
        return meta

    def existing_local_receipt(self, directory, encrypted, file_id="previous-cloud-id", **changes):
        record = {"id": file_id, "name": encrypted.name, "size": encrypted.stat().st_size,
                  "parent": "private-folder", "md5": backup.digest(encrypted, "md5"),
                  "sha256": backup.digest(encrypted), "backup_owner": backup.OWNER,
                  "completed_utc": "2026-10-13T13:05:32Z", "cloud_keep": 3650, "local_keep": 7}
        record.update(changes)
        path = directory / (encrypted.name + ".json")
        original = json.dumps(record)
        path.write_text(original, encoding="utf-8")
        return path, original

    def test_retry_encrypted_uploads_without_archive_or_encryption(self):
        with self.retry_pipeline() as (args, directory, drive, packer, encrypted):
            with mock.patch.object(backup.importlib.util, "spec_from_file_location") as crypto, contextlib.redirect_stdout(io.StringIO()):
                backup.run(args)
            packer.assert_not_called()
            crypto.assert_not_called()
            self.assertEqual(len(drive.created), 1)
            self.assertTrue(encrypted.exists())
            self.assertTrue((directory / (encrypted.name + ".json")).exists())

    def test_retry_reuses_exact_remote_and_existing_receipt_without_duplicate_upload(self):
        with self.retry_pipeline() as (args, directory, drive, packer, encrypted):
            meta = self.existing_remote(drive, encrypted)
            receipt = {"id": meta["id"], "name": encrypted.name, "size": encrypted.stat().st_size,
                "parent": "private-folder", "md5": backup.digest(encrypted, "md5"),
                "sha256": backup.digest(encrypted), "backup_owner": backup.OWNER,
                "completed_utc": "2026-10-13T13:05:32Z", "cloud_keep": 3650, "local_keep": 7}
            receipt_path = directory / (encrypted.name + ".json")
            original = json.dumps(receipt)
            receipt_path.write_text(original, encoding="utf-8")
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                backup.run(args)
                backup.run(args)
            packer.assert_not_called()
            self.assertFalse(drive.created)
            self.assertEqual(receipt_path.read_text(encoding="utf-8"), original)
            self.assertEqual(output.getvalue().count("BACKUP_UPLOAD_REUSED"), 2)

    def test_retry_receipt_missing_cloud_id_never_creates_new_upload(self):
        with self.retry_pipeline() as (args, directory, drive, packer, encrypted):
            receipt_path, original = self.existing_local_receipt(directory, encrypted)
            original_get = drive.get
            def get(fileId, **options):
                if fileId == "previous-cloud-id":
                    return Request(mock.Mock(side_effect=HttpFailure(404, "notFound")))
                return original_get(fileId, **options)
            with mock.patch.object(drive, "get", side_effect=get), contextlib.redirect_stdout(io.StringIO()), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertEqual(failed.exception.code, "retry_receipt_cloud_missing")
            self.assertFalse(drive.created or drive.queries)
            self.assertEqual(receipt_path.read_text(encoding="utf-8"), original)
            self.assertTrue(encrypted.exists())
            packer.assert_not_called()

    def test_retry_receipt_valid_cloud_id_reused_even_when_name_listing_is_empty(self):
        with self.retry_pipeline() as (args, directory, drive, packer, encrypted):
            meta = self.existing_remote(drive, encrypted)
            receipt_path, original = self.existing_local_receipt(directory, encrypted, meta["id"])
            drive.pages = [{"files": []}]
            with contextlib.redirect_stdout(io.StringIO()):
                backup.run(args)
            self.assertFalse(drive.created)
            self.assertEqual(receipt_path.read_text(encoding="utf-8"), original)
            self.assertTrue(encrypted.exists())
            packer.assert_not_called()

    def test_retry_receipt_remote_mismatch_never_creates_or_changes_receipt(self):
        for change in [{"trashed": True}, {"shared": True}, {"md5Checksum": "bad"}, {"size": "0"},
                       {"name": "other.enc"}, {"parents": ["wrong-folder"]},
                       {"appProperties": {"backupOwner": "other-task"}}]:
            with self.subTest(change=change), self.retry_pipeline() as (args, directory, drive, packer, encrypted):
                meta = self.existing_remote(drive, encrypted, **change)
                receipt_path, original = self.existing_local_receipt(directory, encrypted, meta["id"])
                drive.pages = [{"files": []}]
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(backup.BackupFailure):
                    backup.run(args)
                self.assertFalse(drive.created or drive.queries)
                self.assertEqual(receipt_path.read_text(encoding="utf-8"), original)
                self.assertTrue(encrypted.exists())
                packer.assert_not_called()

    def test_retry_invalid_local_receipt_never_looks_up_or_creates_cloud_file(self):
        for change in [{"backup_owner": "other-task"}, {"parent": "other-folder"}, {"sha256": "bad"},
                       {"md5": "bad"}, {"size": 0}, {"name": "other.enc"}, {"id": ""},
                       {"completed_utc": None}]:
            with self.subTest(change=change), self.retry_pipeline() as (args, directory, drive, packer, encrypted):
                receipt_path, original = self.existing_local_receipt(directory, encrypted, **change)
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(backup.BackupFailure) as failed:
                    backup.run(args)
                self.assertEqual(failed.exception.code, "local_receipt_conflict")
                self.assertFalse(drive.created or drive.queries)
                self.assertEqual(receipt_path.read_text(encoding="utf-8"), original)
                self.assertTrue(encrypted.exists())
                packer.assert_not_called()

    def test_retry_cloud_conflict_or_duplicates_refuse_upload_and_keep_ciphertext(self):
        for change in [{"md5Checksum": "bad"}, {"appProperties": {"backupOwner": "other-task"}},
                       {"shared": True}, {"parents": ["another-folder"]}, {"name": "other.enc"},
                       {"size": "0"}, {"duplicate": True}]:
            with self.subTest(change=change), self.retry_pipeline() as (args, directory, drive, packer, encrypted):
                if change.get("duplicate"):
                    meta = self.existing_remote(drive, encrypted)
                    drive.pages[0]["files"].append(dict(meta, id="another-copy"))
                else:
                    self.existing_remote(drive, encrypted, **change)
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(backup.BackupFailure):
                    backup.run(args)
                packer.assert_not_called()
                self.assertFalse(drive.created)
                self.assertTrue(encrypted.exists())
                self.assertFalse((directory / (encrypted.name + ".json")).exists())
                # Subsequent subcases use the same isolated directory.
                for stage in directory.glob(".project-run-*"):
                    backup.shutil.rmtree(str(stage))

    def test_retry_tls_exhaustion_keeps_ciphertext_and_logs_safe_code(self):
        drive = FakeDrive()
        drive.upload_error = ssl.SSLError(8, "FAKE_SECRET_MUST_NOT_BE_LOGGED")
        with self.retry_pipeline(drive) as (args, directory, _, packer, encrypted):
            content, output = encrypted.read_bytes(), io.StringIO()
            with mock.patch.object(backup.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertEqual(failed.exception.code, "drive_tls_transmission_interrupted")
            self.assertFalse(failed.exception.uploaded)
            self.assertEqual(sleep.call_count, 6)
            self.assertEqual(encrypted.read_bytes(), content)
            packer.assert_not_called()
            self.assertFalse((directory / (encrypted.name + ".json")).exists())
            self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_retry_bad_paths_headers_and_public_key_are_rejected_before_network(self):
        with self.retry_pipeline() as (args, directory, _, packer, encrypted):
            for raw in [str(self.root / encrypted.name), str(directory / ("other-" + encrypted.name)), encrypted.name]:
                with self.subTest(raw=raw), mock.patch.object(backup, "drive_service") as service, self.assertRaises(backup.BackupFailure) as failed:
                    args.retry_encrypted = raw
                    backup.run(args)
                self.assertEqual(failed.exception.code, "retry_encrypted_path_invalid")
                service.assert_not_called()
            args.retry_encrypted = str(encrypted)
            for changes in [{"version": True}, {"cipher": "wrong"}, {"nonce": "bad!"}, {"key": base64.b64encode(b"1" * 128).decode("ascii")}, {"key": base64.b64encode(b"\xff" * 384).decode("ascii")}]:
                self.encrypted_envelope(directory, changes)
                with self.subTest(changes=changes), mock.patch.object(backup, "drive_service") as service, self.assertRaises(backup.BackupFailure) as failed:
                    backup.run(args)
                self.assertEqual(failed.exception.code, "retry_encrypted_format_invalid")
                service.assert_not_called()
            self.encrypted_envelope(directory)
            with mock.patch.dict(sys.modules, public_key_modules(key_size=1024)), mock.patch.object(backup, "drive_service") as service, self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertEqual(failed.exception.code, "backup_public_key_invalid")
            service.assert_not_called()
            packer.assert_not_called()

    def test_success_is_after_plaintext_stage_cleanup_and_atomic_receipt(self):
        with self.pipeline() as (args, directory, _, _):
            class VerifySuccessOutput(io.StringIO):
                def write(stream, message):
                    if message.startswith("BACKUP_SUCCESS"):
                        self.assertFalse(list(directory.glob(".project-run-*")))
                    return super().write(message)
            output = VerifySuccessOutput()
            with contextlib.redirect_stdout(output):
                backup.run(args)
            self.assertEqual(output.getvalue().count("BACKUP_SUCCESS"), 1)
            receipts = list(directory.glob(backup.PREFIX + "*.json"))
            self.assertEqual(len(receipts), 1)
            self.assertEqual(json.loads(receipts[0].read_text(encoding="utf-8"))["backup_owner"], backup.OWNER)
            self.assertFalse(list(directory.glob(".project-run-*")))

    def test_upload_verification_failure_leaves_ciphertext_without_receipt(self):
        drive = FakeDrive()
        with self.pipeline(drive) as (args, directory, _, _):
            original_upload = backup.upload_verified
            def invalid_readback(service, path, parent):
                service.overrides = {"md5Checksum": "0" * 32}
                return original_upload(service, path, parent)
            output = io.StringIO()
            with mock.patch.object(backup, "upload_verified", side_effect=invalid_readback), contextlib.redirect_stdout(output):
                with self.assertRaises(RuntimeError):
                    backup.run(args)
            self.assertEqual(len(list(directory.glob(backup.PREFIX + "*.tar.gz.enc"))), 1)
            self.assertFalse(list(directory.glob(backup.PREFIX + "*.json")))
            stages = list(directory.glob(".project-run-*"))
            self.assertEqual(len(stages), 1)
            self.assertFalse(list(stages[0].glob("*.tar.gz")))
            failure = json.loads((stages[0] / "failure.json").read_text(encoding="utf-8"))
            self.assertEqual(failure["code"], "drive_upload_verification_failed")
            self.assertEqual(failure["phase"], "upload")
            self.assertFalse(failure["uploaded"])
            self.assertNotIn("BACKUP_SUCCESS", output.getvalue())

    def test_archive_failure_retains_private_child_diagnostics_and_cannot_report_success(self):
        with self.pipeline() as (args, directory, _, packer):
            def failed_pack(command, **_):
                plaintext = Path(command[command.index("--output") + 1])
                plaintext.write_bytes(b"partial staged archive")
                child = plaintext.parent / ".bt-project-stage-failed"
                child.mkdir()
                (child / "export-private.log").write_text("FAKE_SECRET_MUST_NOT_BE_LOGGED", encoding="utf-8")
                raise subprocess.CalledProcessError(1, "isolated-packer")
            packer.side_effect = failed_pack
            output = io.StringIO()
            with contextlib.redirect_stdout(output), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertIsInstance(failed.exception.__cause__, subprocess.CalledProcessError)
            stages = list(directory.glob(".project-run-*"))
            self.assertEqual(len(stages), 1)
            self.assertTrue((stages[0] / ".bt-project-stage-failed" / "export-private.log").exists())
            failure = json.loads((stages[0] / "failure.json").read_text(encoding="utf-8"))
            self.assertEqual(failure["phase"], "archive")
            self.assertEqual(failure["code"], "archive_failed")
            self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", json.dumps(failure) + output.getvalue())
            self.assertFalse(list(directory.glob(backup.PREFIX + "*.tar.gz.enc")))
            self.assertNotIn("BACKUP_SUCCESS", output.getvalue())

    def test_receipt_write_failure_never_publishes_partial_json(self):
        with self.pipeline() as (args, directory, _, _):
            output = io.StringIO()
            with mock.patch.object(backup.json, "dump", side_effect=OSError("isolated disk full")), contextlib.redirect_stdout(output), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertFalse(list(directory.glob(backup.PREFIX + "*.json")))
            self.assertEqual(len(list(directory.glob(backup.PREFIX + "*.tar.gz.enc"))), 1)
            self.assertEqual(len(list(directory.glob(".project-run-*"))), 1)
            self.assertEqual(failed.exception.phase, "publish_receipt")
            self.assertTrue(failed.exception.uploaded)
            self.assertNotIn("BACKUP_SUCCESS", output.getvalue())

    def test_stage_cleanup_failure_never_reports_success(self):
        with self.pipeline() as (args, directory, _, _):
            output = io.StringIO()
            with mock.patch.object(backup.shutil, "rmtree", side_effect=PermissionError("isolated cleanup failure")), contextlib.redirect_stdout(output), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertNotIn("BACKUP_SUCCESS", output.getvalue())
            self.assertEqual(len(list(directory.glob(backup.PREFIX + "*.tar.gz.enc"))), 1)
            self.assertEqual(failed.exception.phase, "cleanup_stage")
            self.assertTrue(failed.exception.uploaded)

    def test_retention_failure_reports_completed_upload_and_keeps_receipt(self):
        with self.pipeline() as (args, directory, _, _):
            with mock.patch.object(backup, "retain_native", side_effect=ValueError("FAKE_SECRET_MUST_NOT_BE_LOGGED")), contextlib.redirect_stdout(io.StringIO()), self.assertRaises(backup.BackupFailure) as failed:
                backup.run(args)
            self.assertEqual(failed.exception.phase, "retain_native")
            self.assertTrue(failed.exception.uploaded)
            self.assertEqual(len(list(directory.glob(backup.PREFIX + "*.json"))), 1)
            stage = list(directory.glob(".project-run-*"))[0]
            record = json.loads((stage / "failure.json").read_text(encoding="utf-8"))
            self.assertTrue(record["uploaded"])
            self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", json.dumps(record))

    def test_busy_lock_fails_before_authentication_or_archive(self):
        with self.pipeline() as (args, directory, _, packer):
            with mock.patch.object(backup.fcntl, "flock", side_effect=BlockingIOError) as acquire_lock, mock.patch.object(backup, "drive_service") as authenticate:
                with self.assertRaises(RuntimeError):
                    backup.run(args)
            acquire_lock.assert_called_once()
            self.assertEqual(acquire_lock.call_args[0][1], backup.fcntl.LOCK_EX | backup.fcntl.LOCK_NB)
            authenticate.assert_not_called()
            packer.assert_not_called()
            self.assertFalse(list(directory.glob(".project-run-*")))

    def test_main_failure_redacts_library_message_and_exits_nonzero(self):
        output, errors = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", [str(SCRIPT)]), mock.patch.object(backup.os, "umask"), mock.patch.object(backup, "run", side_effect=ValueError("FAKE_SECRET_MUST_NOT_BE_LOGGED")) as run, contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            with self.assertRaises(SystemExit) as exit_result:
                backup.main()
        self.assertEqual(run.call_args[0][0].cloud_keep, 3650)
        self.assertEqual(run.call_args[0][0].local_keep, 7)
        self.assertEqual(exit_result.exception.code, 1)
        self.assertIn("BACKUP_FAILED ValueError", errors.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", errors.getvalue())
        self.assertNotIn("BACKUP_SUCCESS", output.getvalue())

    def test_check_only_never_runs_archive_upload_or_retention(self):
        drive = FakeDrive()
        output = io.StringIO()
        with mock.patch.object(sys, "argv", [str(SCRIPT), "--check-only", "--folder", "private-folder"]), mock.patch.object(backup.os, "umask"), mock.patch.object(backup, "drive_service", return_value=drive), mock.patch.object(backup, "run") as run, contextlib.redirect_stdout(output):
            backup.main()
        run.assert_not_called()
        self.assertIn("CHECK_SUCCESS", output.getvalue())
        self.assertFalse(drive.created or drive.updated or drive.queries)

    def test_nonpositive_retention_rejected_before_authentication(self):
        for flag in ["--cloud-keep", "--local-keep"]:
            with self.subTest(flag=flag), mock.patch.object(sys, "argv", [str(SCRIPT), flag, "0"]), mock.patch.object(backup, "drive_service") as authenticate, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as exit_result:
                backup.main()
            self.assertEqual(exit_result.exception.code, 2)
            authenticate.assert_not_called()


if __name__ == "__main__":
    unittest.main()
