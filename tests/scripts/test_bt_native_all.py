"""Native BaoTa alias contracts; all panel and Drive operations are fake."""
import contextlib
import copy
import errno
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import socket
import ssl
import stat
import sys
import tempfile
import types
import unittest
from unittest import mock

SCRIPT = (Path(__file__).resolve().parents[2] / "scripts/backup/bt-native-all.py")
stub = types.ModuleType("fcntl")
stub.LOCK_EX, stub.LOCK_NB, stub.flock = 2, 4, lambda *_: None
with mock.patch.dict(sys.modules, {"fcntl": stub}):
    spec = importlib.util.spec_from_file_location("native_backup_test_subject", SCRIPT)
    native = importlib.util.module_from_spec(spec)
    previous = sys.dont_write_bytecode
    sys.dont_write_bytecode = True
    try:
        spec.loader.exec_module(native)
    finally:
        sys.dont_write_bytecode = previous


def folder():
    return {"id": native.TARGET_FOLDER, "name": "ganwen.nl", "parents": [native.WEB_ROOT],
            "mimeType": "application/vnd.google-apps.folder", "shared": False, "trashed": False,
            "owners": [{"emailAddress": native.EXPECTED_OWNER}], "capabilities": {"canAddChildren": True}}


class Request:
    def __init__(self, value):
        self.value = value

    def execute(self, **_):
        return copy.deepcopy(self.value() if callable(self.value) else self.value)


class Drive:
    def __init__(self):
        self.records = {native.TARGET_FOLDER: folder()}
        self.upload_overrides, self.read_ids = {}, []
        self.created = []

    def files(self):
        return self

    def get(self, fileId, **_):
        self.read_ids.append(fileId)
        return Request(lambda: dict(self.records[fileId], **(self.upload_overrides if fileId.startswith("upload-") else {})))

    def list(self, q, **_):
        parent, name = re.search(r"^'([^']+)' in parents and name='([^']+)'", q).groups()
        return Request({"files": [value for value in self.records.values()
                                 if value.get("parents") == [parent] and value.get("name") == name]})

    def create(self, body, **_):
        def created():
            self.created.append(copy.deepcopy(body))
            result = dict(folder(), **body, id="created-" + str(len(self.created)))
            self.records[result["id"]] = result
            return {"id": result["id"]}
        return Request(created)

    def delete(self, **_):
        raise AssertionError("Native wrapper must never permanently delete Drive files")


class Query:
    def __init__(self, rows):
        self.rows = rows

    def where(self, expression, values):
        if expression == "echo=?":
            self.rows = [row for row in self.rows if native.field(row, "echo") == values[0]]
        elif expression == "sType=? AND sName=?":
            self.rows = [row for row in self.rows if native.field(row, "sType") == values[0] and native.field(row, "sName") == values[1]]
        return self

    def field(self, _):
        return self

    def select(self):
        return copy.deepcopy(self.rows)


class Client:
    def __init__(self, service):
        self.service, self.original_paths, self.uploads = service, [], []
        self.bad_target_path, self.no_id = False, False
        self._gdrive_main__creds = object()

    def _create_folder_cycle(self, logical):
        self.original_paths.append(logical)
        return native.TARGET_FOLDER if logical.rstrip("/").endswith("/ganwen.nl") else "other-original-folder"

    def build_object_name(self, data_type, filename):
        assert data_type == "site"
        site = re.fullmatch(r"web_(.+)_20261007_000000_a1\.tar\.gz", Path(filename).name).group(1)
        if self.bad_target_path and site == native.SOURCE_SITE:
            return "bt_backup/web/"
        return "bt_backup/web/" + site + "/"

    def upload_file(self, get=None, data_type=None):
        path = Path(get if isinstance(get, str) else get.filename)
        logical = self.build_object_name(data_type, str(path)) if isinstance(get, str) else get.filepath
        parent = self._create_folder_cycle(logical)
        content = path.read_bytes()
        file_id = "upload-{}".format(len(self.uploads) + 1)
        self.uploads.append((path.name, parent))
        self.service.records[file_id] = {"id": file_id, "name": path.name, "parents": [parent],
            "size": str(len(content)), "md5Checksum": hashlib.md5(content).hexdigest(),
            "shared": False, "trashed": False, "owners": [{"emailAddress": native.EXPECTED_OWNER}]}
        print("Upload Success ,File ID: " + ("None" if self.no_id else file_id))
        return True


class Contracts(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="bt-native-all-offline-")
        self.root = Path(self.temporary.name).resolve()
        self.addCleanup(self.temporary.cleanup)
        self.service = Drive()
        self.client = Client(self.service)
        self.rows = [{"echo": "original-site-echo", "sType": "site", "sName": "ALL",
                      "backupTo": "gdrive", "save": 3650, "save_local": 0, "split_type": "0", "status": 0}]
        public = types.ModuleType("public")
        self.databases = [{"name": "ganwen"}, {"name": "language"}]
        public.M = lambda table: Query(self.databases if table == "databases" else
            [{"name": name} for name in self.sites] if table == "sites" else self.rows)
        public.get_backup_path = lambda: str(self.root / "backup")
        self.instances, self.fail_sites = [], set()
        self.sites = ["ganwen.nl", "other.nl", native.SOURCE_SITE]
        test = self

        class Storage:
            backup_path = "bt_backup"
            def run(self, name):
                raise AssertionError("native DB must reuse the authenticated official client")

        class Backup:
            def __init__(self, cloud_object, cron_info):
                self._cloud, self.cron_info = cloud_object, cron_info
                self._is_save_local, self._error_msg = True, ""
                self.calls, self.cleanup_calls, self.status_calls = [], [], []
                test.instances.append(self)

            def backup_site(self, name, save=3, exclude=None, echo_id=None):
                self.calls.append((name, save, echo_id))
                if name in test.fail_sites:
                    raise OSError("FAKE_SECRET_MUST_NOT_BE_LOGGED")
                directory = test.root / "backup" / "site" / name
                directory.mkdir(parents=True, exist_ok=True)
                path = directory / ("web_" + name + "_20261007_000000_a1.tar.gz")
                path.write_bytes(b"isolated native archive " + name.encode("utf-8"))
                if not self._cloud.upload_file(str(path), "site"):
                    if not self._is_save_local and path.exists():
                        path.unlink()
                    self._error_msg = "Native upload failed"
                    return False
                self.delete_old([], save, "site", name)
                if not self._is_save_local:
                    path.unlink()
                return str(path)

            def backup_site_all(self, save=3, echo_id=None):
                for name in test.sites:
                    self.backup_site(name, save=save, echo_id=echo_id)
                return None  # Real PanelBackup ALL also returns None on failure.

            def backup_database(self, name, save=3, echo_id=None):
                self.calls.append((name, save, echo_id))
                if name in test.fail_sites:
                    return False, "FAKE_SECRET_MUST_NOT_BE_LOGGED"
                path = test.root / ("db_" + name + "_20261007_000000_a1.sql.gz")
                path.write_bytes(b"native compressed mysql " + name.encode("utf-8"))
                result = self._cloud.upload_file(str(path), "bt_backup/database/mysql/" + name + "/" + path.name)
                if not self._is_save_local:
                    path.unlink()
                if not result:
                    return False, "Native upload failed"
                self.delete_old([], save, "database", name)
                return True, str(path)

            def backup_database_all(self, save=3, echo_id=None):
                # Model the real method's fresh CloudStoraUpload construction.
                self._cloud_new = Storage()
                self._cloud = self._cloud_new.run("gdrive")
                for database in test.databases:
                    self.backup_database(database["name"], save=save, echo_id=echo_id)

            def delete_old(self, backups, save, data_type=None, site_name=None):
                self.cleanup_calls.append((save, data_type, site_name, self._cloud))
                if self._cloud is not None:
                    raise AssertionError("Native permanent cloud cleanup reached")

            def save_backup_status(self, *args, **kwargs):
                self.status_calls.append((args, kwargs))

        self.runtime = types.SimpleNamespace(public=public, backup_class=Backup,
                                             client=self.client, service=self.service, storage=Storage(), storage_class=Storage)
        self.args = types.SimpleNamespace(echo=None, save=3650, site="ALL", check_only=False,
                                         panel=str(self.root), lock_dir=str(self.root / "locks"))

    def database_mode(self):
        self.args.kind = "database"
        self.rows = [{"echo": "original-db-echo", "sType": "database", "sName": "ALL",
                      "backupTo": "gdrive", "save": 3650, "save_local": 0, "split_type": "null", "db_type": "mysql"}]
        self.service.records[native.DATABASE_ROOT] = dict(folder(), id=native.DATABASE_ROOT, name="crontab_backup", parents=[native.MYSQL_ROOT])
        for name, parent in [("ganwen", native.DATABASE_ROOT), ("language", native.WEB_ROOT)]:
            self.service.records["db-folder-" + name] = dict(folder(), id="db-folder-" + name, name=name, parents=[parent])

    def execute(self):
        output, errors = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            native.execute(self.args, self.runtime)
        return output.getvalue() + errors.getvalue()

    def test_all_uses_old_folder_only_for_go_and_preserves_other_paths(self):
        original_folder, original_upload = self.client._create_folder_cycle, self.client.upload_file
        output = self.execute()
        self.assertEqual(self.client.uploads, [
            ("web_ganwen.nl_20261007_000000_a1.tar.gz", native.TARGET_FOLDER),
            ("web_other.nl_20261007_000000_a1.tar.gz", "other-original-folder"),
            ("web_ganwen_blog_20261007_000000_a1.tar.gz", native.TARGET_FOLDER)])
        self.assertEqual(self.client.original_paths, ["bt_backup/web/ganwen.nl/", "bt_backup/web/other.nl/"])
        self.assertEqual(self.client._create_folder_cycle, original_folder)
        self.assertEqual(self.client.upload_file, original_upload)
        tool = self.instances[0]
        self.assertFalse(tool._is_save_local)
        self.assertTrue(all(save == 3650 and echo == "original-site-echo" for _, save, echo in tool.calls))
        self.assertTrue(all(call[3] is None for call in tool.cleanup_calls))
        self.assertIs(tool._cloud, self.client)
        self.assertEqual(output.count("NATIVE_UPLOAD_VERIFIED"), 3)
        self.assertEqual(output.count("NATIVE_BACKUP_SUCCESS"), 1)
        self.assertFalse(list(self.root.glob("*.tar.gz")))

    def test_only_site_runs_go_and_persists_native_status(self):
        self.args.site = native.SOURCE_SITE
        self.execute()
        self.assertEqual([site for site, _, _ in self.instances[0].calls], [native.SOURCE_SITE])
        self.assertFalse(self.client.original_paths)
        self.assertEqual(self.client.uploads[0][1], native.TARGET_FOLDER)
        self.assertEqual(self.instances[0].status_calls[0][0], (True,))

    def test_native_mysql_uses_original_folders_and_official_storage(self):
        self.database_mode()
        original = self.runtime.storage_class.run
        output = self.execute()
        self.assertEqual(self.client.uploads, [
            ("db_ganwen_20261007_000000_a1.sql.gz", "db-folder-ganwen"),
            ("db_language_20261007_000000_a1.sql.gz", "db-folder-language")])
        self.assertFalse(self.client.original_paths)
        self.assertFalse(self.service.created)
        self.assertEqual(self.runtime.storage_class.run, original)
        self.assertTrue(all(save == 3650 and echo == "original-db-echo" for _, save, echo in self.instances[0].calls))
        self.assertTrue(all(call[3] is None for call in self.instances[0].cleanup_calls))
        self.assertIn('"kind": "database"', output)
        self.assertIn('"databases": ["ganwen", "language"]', output)
        self.assertEqual(output.count("NATIVE_UPLOAD_VERIFIED"), 2)
        self.assertFalse(list(self.root.glob("*.sql.gz")))

    def test_native_mysql_check_only_is_read_only_and_missing_new_folder_is_planned(self):
        self.database_mode()
        self.args.check_only = True
        self.databases.append({"name": "new_database"})
        output = self.execute()
        self.assertIn('"new_database": null', output)
        self.assertFalse(self.service.created or self.instances or self.client.uploads)

    def test_new_mysql_database_uses_verified_original_parent_and_language_never_moves(self):
        self.database_mode()
        self.databases.append({"name": "new_database"})
        self.execute()
        self.assertEqual(self.service.created, [{"name": "new_database", "mimeType": "application/vnd.google-apps.folder", "parents": [native.DATABASE_ROOT]}])
        self.assertEqual(self.client.uploads[-1][1], "created-1")
        self.assertEqual(self.client.uploads[1][1], "db-folder-language")

    def test_language_original_folder_must_not_be_recreated_or_replaced(self):
        self.database_mode()
        del self.service.records["db-folder-language"]
        self.service.records["wrong-new-language"] = dict(folder(), id="wrong-new-language", name="language", parents=[native.DATABASE_ROOT])
        with self.assertRaises(native.NativeFailure) as error:
            self.execute()
        self.assertEqual(error.exception.code, "language_legacy_folder_missing")
        self.assertFalse(self.service.created or self.client.uploads or self.instances)

    def test_mysql_identity_mismatch_and_partial_backup_cannot_claim_success(self):
        self.database_mode()
        original = copy.deepcopy(self.service.records["db-folder-ganwen"])
        self.service.records["db-folder-ganwen"]["shared"] = True
        with self.assertRaises(native.NativeFailure):
            self.execute()
        self.assertFalse(self.instances or self.client.uploads)
        self.service.records["db-folder-ganwen"] = original
        self.fail_sites.add("ganwen")
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(RuntimeError):
            native.execute(self.args, self.runtime)
        self.assertEqual([call[0] for call in self.instances[-1].calls], ["ganwen", "language"])
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_check_only_does_not_construct_native_backup_or_upload(self):
        self.args.check_only = True
        output = self.execute()
        self.assertIn("NATIVE_CHECK_SUCCESS", output)
        self.assertFalse(self.instances or self.client.uploads or self.client.original_paths)
        self.assertEqual(self.service.read_ids, [native.TARGET_FOLDER])

    def test_paused_original_task_still_supplies_configuration_without_mutation(self):
        before = copy.deepcopy(self.rows)
        self.assertEqual(native.cron_template(self.runtime.public, None, 3650), "original-site-echo")
        self.assertEqual(self.rows, before)
        self.rows.append(dict(self.rows[0], echo="second-original"))
        with self.assertRaises(RuntimeError):
            native.cron_template(self.runtime.public, None, 3650)
        self.assertEqual(native.cron_template(self.runtime.public, "original-site-echo", 3650), "original-site-echo")

    def test_shell_database_wrong_count_local_or_split_template_rejected(self):
        original = copy.deepcopy(self.rows[0])
        for changes in [{"sType": "toShell"}, {"sType": "database"}, {"sName": "one.nl"},
                        {"backupTo": "another-provider"}, {"save": 10}, {"save_local": 1}, {"split_type": "size"}]:
            with self.subTest(changes=changes):
                self.rows = [dict(original, **changes)]
                with self.assertRaises(RuntimeError):
                    native.cron_template(self.runtime.public, None, 3650)

    def test_folder_wrong_identity_owner_sharing_parent_or_permissions_rejected(self):
        for changes in [{"id": "wrong-folder"}, {"name": "ganwen_blog"}, {"parents": ["wrong-root"]},
                        {"shared": True}, {"shared": None}, {"trashed": True},
                        {"owners": [{"emailAddress": "another@example.invalid"}]},
                        {"owners": [{"emailAddress": native.EXPECTED_OWNER}, {"emailAddress": "extra@example.invalid"}]},
                        {"capabilities": {"canAddChildren": False}}]:
            with self.subTest(changes=changes):
                self.service.records[native.TARGET_FOLDER] = dict(folder(), **changes)
                with self.assertRaises(RuntimeError):
                    self.execute()
                self.assertFalse(self.instances or self.client.uploads)

    def test_unexpected_go_logical_path_never_creates_new_folder(self):
        self.args.site, self.client.bad_target_path = native.SOURCE_SITE, True
        with self.assertRaises(RuntimeError):
            self.execute()
        self.assertFalse(self.client.uploads or self.client.original_paths)

    def test_failure_continues_other_sites_but_cannot_claim_all_success(self):
        self.fail_sites.add("other.nl")
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(RuntimeError):
            native.execute(self.args, self.runtime)
        self.assertEqual([name for name, _, _ in self.instances[0].calls], self.sites)
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_upload_missing_id_or_bad_readback_does_not_count_as_success(self):
        self.args.site = native.SOURCE_SITE
        for change in [{"size": "0"}, {"md5Checksum": "0" * 32}, {"parents": ["wrong-folder"]},
                       {"shared": True}, {"trashed": True}, {"name": "wrong-name"},
                       {"owners": [{"emailAddress": "someone@example.invalid"}]}]:
            with self.subTest(change=change):
                self.service.upload_overrides = change
                output = io.StringIO()
                with contextlib.redirect_stdout(output), self.assertRaises(RuntimeError):
                    native.execute(self.args, self.runtime)
                self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
                self.assertFalse(list(self.root.glob("*.tar.gz")))
                retained = list((self.root / "backup" / "site" / native.SOURCE_SITE / ".failed-uploads").glob("*.tar.gz"))
                self.assertEqual(len(retained), 1)
                retained[0].unlink()
        self.service.upload_overrides, self.client.no_id = {}, True
        with self.assertRaises(RuntimeError):
            self.execute()
        self.assertFalse(list(self.root.glob("*.tar.gz")))

    def test_upload_transport_failure_retains_hardlink_before_native_cleanup(self):
        self.args.site = native.SOURCE_SITE
        output = io.StringIO()
        with mock.patch.object(self.client, "upload_file", side_effect=OSError("FAKE_SECRET_MUST_NOT_BE_LOGGED")), contextlib.redirect_stdout(output), self.assertRaises(RuntimeError):
            native.execute(self.args, self.runtime)
        self.assertIn("NATIVE_UPLOAD_FAILED", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
        self.assertFalse(list(self.root.glob("*.tar.gz")))
        directory = self.root / "backup" / "site" / native.SOURCE_SITE
        self.assertFalse(list(directory.glob("*.tar.gz")))
        retained = list((directory / ".failed-uploads").glob("*.tar.gz"))
        self.assertEqual(len(retained), 1)
        self.assertEqual(retained[0].read_bytes(), b"isolated native archive " + native.SOURCE_SITE.encode("utf-8"))
        self.assertIn('"archive_retained": true', output.getvalue())

    def archive(self, site="other.nl", failed=False):
        directory = self.root / "backup" / "site" / site
        if failed:
            directory /= ".failed-uploads"
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / ("web_" + site + "_20261007_000000_a1.tar.gz")
        path.write_bytes(b"existing native archive " + site.encode("utf-8"))
        return path

    def site_folder(self, site):
        if site == native.SOURCE_SITE:
            return native.TARGET_FOLDER
        file_id = "site-folder-" + site
        self.service.records[file_id] = dict(folder(), id=file_id, name=site)
        return file_id

    def test_single_registered_site_and_unregistered_path_boundaries(self):
        self.args.site = "other.nl"
        output = self.execute()
        self.assertEqual([name for name, _, _ in self.instances[0].calls], ["other.nl"])
        summary = json.loads(next(line.split(" ", 1)[1] for line in output.splitlines() if line.startswith("NATIVE_BACKUP_SUCCESS ")))
        self.assertEqual(summary["sites"], ["other.nl"])
        for candidate in ("missing", "../other.nl", "/www/wwwroot/other.nl"):
            self.args.site = candidate
            before = len(self.instances)
            with self.subTest(candidate=candidate), self.assertRaises(native.NativeFailure) as failure:
                self.execute()
            self.assertEqual(failure.exception.code, "native_site_not_registered")
            self.assertEqual(len(self.instances), before)

    def test_duplicate_registered_site_names_refuse_execution(self):
        self.sites.append("other.nl")
        with self.assertRaises(native.NativeFailure) as failure:
            self.execute()
        self.assertEqual(failure.exception.code, "native_site_names_ambiguous")
        self.assertFalse(self.instances or self.client.uploads)

    def test_failed_archive_link_is_same_file_private_and_survives_source_unlink(self):
        path = self.archive()
        retained = native.retain_failed_archive(str(path), self.runtime.public, set(self.sites), "other.nl")
        self.assertTrue(os.path.samefile(str(path), str(retained)))
        expected = path.read_bytes()
        path.unlink()
        self.assertEqual(retained.read_bytes(), expected)
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(retained.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(retained.parent.stat().st_mode), 0o700)

    def test_hardlink_failure_reports_not_retained_without_secret_or_success(self):
        self.args.site = native.SOURCE_SITE
        output = io.StringIO()
        with mock.patch.object(self.client, "upload_file", side_effect=ConnectionResetError(errno.ECONNRESET, "FAKE_SECRET_MUST_NOT_BE_LOGGED")), mock.patch.object(native.os, "link", side_effect=OSError(errno.EXDEV, "FAKE_SECRET_MUST_NOT_BE_LOGGED")), contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.execute(self.args, self.runtime)
        self.assertIn("failed_archive_hardlink_failed", output.getvalue())
        self.assertIn('"archive_retained": false', output.getvalue())
        self.assertNotIn('"archive_retained": true', output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
        target = failure.exception.summary["failed_targets"][0]
        self.assertEqual(target["error_type"], "ConnectionResetError")
        self.assertEqual(target["errno"], errno.ECONNRESET)
        self.assertEqual(target["retention_code"], "failed_archive_hardlink_failed")

    def test_retry_two_existing_archives_never_recompress_or_claim_all_success(self):
        paths = [self.archive("other.nl", failed=True), self.archive(native.SOURCE_SITE)]
        self.site_folder("other.nl")
        self.args.retry_archives = [str(path) for path in paths]
        output = self.execute()
        self.assertFalse(self.instances[0].calls or self.instances[0].cleanup_calls or self.instances[0].status_calls)
        self.assertEqual(len(self.client.uploads), 2)
        self.assertTrue(all(path.exists() for path in paths))
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output)
        summary = json.loads(next(line.split(" ", 1)[1] for line in output.splitlines() if line.startswith("NATIVE_RETRY_SUCCESS ")))
        self.assertEqual(summary["targets"], ["other.nl", native.SOURCE_SITE])
        self.assertEqual(summary["count"], 2)
        self.assertTrue(summary["archives_retained"])

    def test_retry_existing_cloud_archive_is_exactly_verified_before_reuse(self):
        path = self.archive("other.nl", failed=True)
        parent = self.site_folder("other.nl")
        self.service.records["upload-existing"] = {"id": "upload-existing", "name": path.name,
            "parents": [parent], "size": str(path.stat().st_size), "md5Checksum": hashlib.md5(path.read_bytes()).hexdigest(),
            "shared": False, "trashed": False, "owners": [{"emailAddress": native.EXPECTED_OWNER}]}
        self.args.retry_archives = [str(path)]
        output = self.execute()
        self.assertFalse(self.client.uploads)
        self.assertIn("NATIVE_ARCHIVE_REUSED", output)
        self.assertIn("NATIVE_RETRY_SUCCESS", output)
        self.assertTrue(path.exists())
        for change in [{"md5Checksum": "bad"}, {"size": "0"}, {"shared": True},
                       {"owners": [{"emailAddress": "other@example.invalid"}]}]:
            self.service.upload_overrides = change
            with self.subTest(change=change), self.assertRaises(native.NativeFailure):
                self.execute()
            self.assertFalse(self.client.uploads)
            self.assertTrue(path.exists())

    def test_retry_ambiguous_cloud_archive_refuses_duplicate_upload(self):
        path = self.archive("other.nl")
        parent = self.site_folder("other.nl")
        for index in range(2):
            self.service.records["existing-" + str(index)] = {"id": "existing-" + str(index), "name": path.name, "parents": [parent]}
        self.args.retry_archives = [str(path)]
        with self.assertRaises(native.NativeFailure) as failure:
            self.execute()
        self.assertEqual(failure.exception.summary["failed_targets"][0]["code"], "native_archive_cloud_ambiguous")
        self.assertFalse(self.client.uploads)
        self.assertTrue(path.exists())

    def test_retry_wrong_archive_path_name_and_site_are_rejected_before_upload(self):
        valid = self.archive("other.nl")
        outside = self.root / valid.name
        outside.write_bytes(valid.read_bytes())
        wrong = valid.with_name("wrong-name.tar.gz")
        wrong.write_bytes(valid.read_bytes())
        unknown = self.archive("unregistered")
        for path in (outside, wrong, unknown):
            self.args.retry_archives = [str(path)]
            with self.subTest(path=str(path)), self.assertRaises(native.NativeFailure):
                self.execute()
            self.assertFalse(self.client.uploads or self.instances)

    def test_retry_failure_keeps_archive_and_does_not_claim_any_scope_success(self):
        path = self.archive(native.SOURCE_SITE, failed=True)
        self.args.retry_archives = [str(path)]
        output = io.StringIO()
        with mock.patch.object(self.client, "upload_file", side_effect=TimeoutError("FAKE_SECRET_MUST_NOT_BE_LOGGED")), contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.execute(self.args, self.runtime)
        self.assertEqual(failure.exception.summary["targets"], [native.SOURCE_SITE])
        self.assertEqual(failure.exception.summary["failed_targets"][0]["error_type"], "TimeoutError")
        self.assertTrue(path.exists())
        self.assertNotIn("NATIVE_RETRY_SUCCESS", output.getvalue())
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_retry_partial_success_reports_both_targets_without_all_success(self):
        paths = [self.archive("other.nl", failed=True), self.archive(native.SOURCE_SITE, failed=True)]
        self.site_folder("other.nl")
        self.args.retry_archives = [str(path) for path in paths]
        original_upload = self.client.upload_file
        def upload(path, kind):
            if native.SOURCE_SITE in path:
                raise TimeoutError("FAKE_SECRET_MUST_NOT_BE_LOGGED")
            return original_upload(path, kind)
        output = io.StringIO()
        with mock.patch.object(self.client, "upload_file", side_effect=upload), contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.execute(self.args, self.runtime)
        self.assertEqual(failure.exception.summary["targets"], ["other.nl", native.SOURCE_SITE])
        self.assertEqual(failure.exception.summary["successful_targets"], ["other.nl"])
        self.assertEqual(failure.exception.summary["failed_targets"][0]["site"], native.SOURCE_SITE)
        self.assertTrue(all(path.exists() for path in paths))
        self.assertNotIn("NATIVE_RETRY_SUCCESS", output.getvalue())
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())

    def test_retry_symlink_archive_is_refused(self):
        valid = self.archive("other.nl")
        outside = self.root / "outside.bin"
        outside.write_bytes(valid.read_bytes())
        valid.unlink()
        try:
            valid.symlink_to(outside)
        except (OSError, NotImplementedError):
            self.skipTest("Host cannot create symlinks")
        self.args.retry_archives = [str(valid)]
        with self.assertRaises(native.NativeFailure):
            self.execute()
        self.assertFalse(self.client.uploads or self.instances)

    def test_busy_nonblocking_lock_prevents_runtime_authentication(self):
        with mock.patch.object(native.fcntl, "flock", side_effect=BlockingIOError) as lock, mock.patch.object(native, "load_runtime") as load:
            with self.assertRaises(RuntimeError):
                native.run(self.args)
        lock.assert_called_once()
        self.assertEqual(lock.call_args[0][1], native.fcntl.LOCK_EX | native.fcntl.LOCK_NB)
        load.assert_not_called()

    def test_runtime_reuses_installed_cloud_factory_and_original_credential_scope(self):
        storage = types.SimpleNamespace(run=mock.Mock(return_value=self.client))
        cloud = types.ModuleType("CloudStoraUpload")
        def make_storage():
            print("FAKE_AUTH_DIAGNOSTIC")
            return storage
        cloud.CloudStoraUpload = make_storage
        panel = types.ModuleType("panelBackup")
        panel.backup = self.runtime.backup_class
        google = types.ModuleType("googleapiclient")
        google.__path__ = []
        discovery = types.ModuleType("googleapiclient.discovery")
        discovery.build = mock.Mock(return_value=self.service)
        old_cwd, old_path, output = Path.cwd(), sys.path[:], io.StringIO()
        try:
            with mock.patch.dict(sys.modules, {"public": self.runtime.public, "panelBackup": panel,
                    "CloudStoraUpload": cloud, "googleapiclient": google, "googleapiclient.discovery": discovery}), contextlib.redirect_stdout(output):
                runtime = native.load_runtime(self.root)
        finally:
            native.os.chdir(str(old_cwd))
            sys.path[:] = old_path
        self.assertIs(runtime.client, self.client)
        storage.run.assert_called_once_with("gdrive")
        discovery.build.assert_called_once_with("drive", "v3", credentials=self.client._gdrive_main__creds, cache_discovery=False)
        self.assertNotIn("scopes", discovery.build.call_args[1])
        self.assertNotIn("FAKE_AUTH_DIAGNOSTIC", output.getvalue())

    def test_main_redacts_failure_and_returns_nonzero(self):
        output, errors = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", [str(SCRIPT)]), mock.patch.object(native.os, "umask"), mock.patch.object(native, "run", side_effect=OSError("FAKE_SECRET_MUST_NOT_BE_LOGGED")), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            with self.assertRaises(SystemExit) as result:
                native.main()
        self.assertEqual(result.exception.code, 1)
        self.assertIn("NATIVE_BACKUP_FAILED OSError", errors.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", errors.getvalue())
        self.assertNotIn("NATIVE_BACKUP_SUCCESS", output.getvalue())


class DriveRetries(unittest.TestCase):
    def setUp(self):
        patch = mock.patch.object(native, "MIN_DRIVE_INTERVAL", 0)
        patch.start()
        self.addCleanup(patch.stop)

    @staticmethod
    def error(status, reason="userRateLimitExceeded", message="FAKE_SECRET_MUST_NOT_BE_LOGGED"):
        error = OSError("FAKE_SECRET_MUST_NOT_BE_LOGGED")
        error.resp = types.SimpleNamespace(status=status)
        error.content = json.dumps({"error": {"message": message, "errors": [{"reason": reason}]}}).encode()
        return error

    def test_quota_retries_same_request_with_bounded_backoff_and_redaction(self):
        request = types.SimpleNamespace(execute=mock.Mock(side_effect=[self.error(403), self.error(429), {"id": "same-upload"}]))
        output = io.StringIO()
        with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output):
            result = native.RetryingRequest(request, "drive_create").execute()
        self.assertEqual(result, {"id": "same-upload"})
        self.assertEqual(request.execute.call_count, 3)
        self.assertEqual(sleep.call_args_list, [mock.call(1), mock.call(2)])
        self.assertEqual(output.getvalue().count("NATIVE_DRIVE_RETRY"), 2)
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_retry_budget_and_permanent_errors_have_safe_phase_and_code(self):
        for status, reason, attempts, expected in [
            (429, "userRateLimitExceeded", native.DRIVE_ATTEMPTS, "drive_rate_limited"),
            (403, "dailyLimitExceeded", 1, "drive_access_or_quota_denied"),
            (403, "storageQuotaExceeded", 1, "drive_storage_quota_exceeded"),
            (401, "authError", 1, "drive_authentication_failed")]:
            with self.subTest(status=status):
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=self.error(status, reason)))
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(io.StringIO()), self.assertRaises(native.NativeFailure) as error:
                    native.drive_request(request, "drive_create")
                self.assertEqual(request.execute.call_count, attempts)
                self.assertEqual(error.exception.phase, "drive_create")
                self.assertEqual(error.exception.code, expected)
                self.assertTrue(all(call[0][0] <= 32 for call in sleep.call_args_list))

    def test_quota_exceeded_is_retried_only_when_explicitly_per_minute(self):
        self.assertEqual(native.drive_code(self.error(403, "quotaExceeded", "Quota exceeded: requests per minute")), ("drive_rate_limited", True))
        self.assertEqual(native.drive_code(self.error(403, "quotaExceeded", "Daily upload quota exceeded")), ("drive_access_or_quota_denied", False))
        self.assertEqual(native.drive_code(self.error(403, "", "Quota exceeded for quota metric 'Queries' and limit 'Requests per minute'")), ("drive_rate_limited", True))
        self.assertEqual(native.drive_code(self.error(403, "storageQuotaExceeded", "Quota exceeded for quota metric 'Queries' per minute")), ("drive_storage_quota_exceeded", False))

    def test_official_module_service_adaptation_restores_module_after_failure(self):
        request = types.SimpleNamespace(execute=mock.Mock(return_value={"id": "native-upload"}))
        files = types.SimpleNamespace(create=mock.Mock(return_value=request))
        service = types.SimpleNamespace(files=lambda: files)
        module = types.ModuleType("fake_official_gdrive")
        module.build = mock.Mock(return_value=service)
        ClientClass = type("ClientClass", (), {"__module__": module.__name__})
        original = module.build
        with mock.patch.dict(sys.modules, {module.__name__: module}):
            with self.assertRaises(OSError):
                with native.official_drive_retries(ClientClass()):
                    result = module.build("drive", "v3").files().create(body={"name": "archive"}).execute()
                    self.assertEqual(result, {"id": "native-upload"})
                    raise OSError("failure")
        self.assertIs(module.build, original)
        request.execute.assert_called_once_with(num_retries=0)

    def test_request_pacing_limits_sequential_operations(self):
        request = types.SimpleNamespace(execute=mock.Mock(return_value={"id": "same"}))
        with mock.patch.object(native, "MIN_DRIVE_INTERVAL", 1.1), mock.patch.object(native, "_last_drive_request", 0), mock.patch.object(native.time, "monotonic", side_effect=[100, 100, 100, 101.1]), mock.patch.object(native.time, "sleep") as sleep:
            native.drive_request(request, "drive_get")
            native.drive_request(request, "drive_get")
        sleep.assert_called_once_with(1.1)

    def test_tls_eof_retries_same_upload_without_logging_error_details(self):
        error = ssl.SSLEOFError(ssl.SSL_ERROR_EOF, "FAKE_SECRET_MUST_NOT_BE_LOGGED")
        request = types.SimpleNamespace(execute=mock.Mock(side_effect=[error, {"id": "same-upload"}]))
        output = io.StringIO()
        with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output):
            result = native.drive_request(request, "drive_create")
        self.assertEqual(result, {"id": "same-upload"})
        self.assertEqual(request.execute.call_count, 2)
        sleep.assert_called_once_with(1)
        self.assertIn("drive_tls_connection_interrupted", output.getvalue())
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_tls_bad_record_mac_is_transient_but_certificate_and_protocol_errors_are_not(self):
        transient = ssl.SSLError(ssl.SSL_ERROR_SSL, "FAKE_SECRET_MUST_NOT_BE_LOGGED")
        transient.reason = "DECRYPTION_FAILED_OR_BAD_RECORD_MAC"
        self.assertEqual(native.drive_code(transient), ("drive_tls_connection_interrupted", True))
        for error in [ssl.SSLCertVerificationError(ssl.SSL_ERROR_SSL, "FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                      ssl.SSLError(ssl.SSL_ERROR_SSL, "FAKE_SECRET_MUST_NOT_BE_LOGGED")]:
            request = types.SimpleNamespace(execute=mock.Mock(side_effect=error))
            with mock.patch.object(native.time, "sleep") as sleep, self.assertRaises(native.NativeFailure):
                native.drive_request(request, "drive_create")
            request.execute.assert_called_once_with(num_retries=0)
            sleep.assert_not_called()

    def test_legacy_ssl_args_known_transport_tokens_retry_same_request_and_redact_message(self):
        for reason in ("DECRYPTION_FAILED_OR_BAD_RECORD_MAC", "SSLV3_ALERT_BAD_RECORD_MAC",
                       "UNEXPECTED_EOF_WHILE_READING", "TLSV1_ALERT_INTERNAL_ERROR"):
            with self.subTest(reason=reason):
                source = ssl.SSLError(ssl.SSL_ERROR_SSL,
                    "[SSL: " + reason + "] FAKE_SECRET_MUST_NOT_BE_LOGGED (_ssl.c:852)")
                self.assertIsNone(getattr(source, "reason", None))
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=[source, {"id": "same-upload"}]))
                output = io.StringIO()
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output):
                    self.assertEqual(native.drive_request(request, "drive_get"), {"id": "same-upload"})
                self.assertEqual(request.execute.call_args_list,
                                 [mock.call(num_retries=0), mock.call(num_retries=0)])
                sleep.assert_called_once_with(1)
                details = json.loads(output.getvalue().strip().split(" ", 1)[1])
                self.assertEqual(details["ssl_reason"], reason)
                self.assertEqual(details["error_type"], "SSLError")
                self.assertEqual(details["code"], "drive_tls_connection_interrupted")
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_legacy_ssl_args_standard_eof_is_bounded_and_safe(self):
        source = ssl.SSLError(ssl.SSL_ERROR_SSL,
            "EOF occurred in violation of protocol (_ssl.c:852) FAKE_SECRET_MUST_NOT_BE_LOGGED")
        request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
        output = io.StringIO()
        with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.drive_request(request, "drive_get")
        self.assertEqual(request.execute.call_count, native.DRIVE_ATTEMPTS)
        self.assertEqual(sleep.call_args_list, [mock.call(1), mock.call(2), mock.call(4),
                                              mock.call(8), mock.call(16), mock.call(32)])
        self.assertEqual(native.safe_error_fields(failure.exception)["ssl_reason"],
                         "EOF_OCCURRED_IN_VIOLATION_OF_PROTOCOL")
        self.assertEqual(failure.exception.code, "drive_tls_connection_interrupted")
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_legacy_ssl_args_certificate_has_priority_over_transient_tokens(self):
        sources = [ssl.SSLError(ssl.SSL_ERROR_SSL,
                   "[SSL: CERTIFICATE_VERIFY_FAILED] FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                   ssl.SSLError(ssl.SSL_ERROR_SSL,
                   "[SSL: UNEXPECTED_EOF_WHILE_READING] CERTIFICATE_VERIFY_FAILED FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                   ssl.SSLCertVerificationError(ssl.SSL_ERROR_SSL,
                   "[SSL: UNEXPECTED_EOF_WHILE_READING] FAKE_SECRET_MUST_NOT_BE_LOGGED")]
        sources[1].reason = "UNEXPECTED_EOF_WHILE_READING"
        verified = ssl.SSLError(ssl.SSL_ERROR_SSL,
            "[SSL: UNEXPECTED_EOF_WHILE_READING] FAKE_SECRET_MUST_NOT_BE_LOGGED")
        verified.verify_code = 20
        sources.append(verified)
        for source in sources:
            with self.subTest(error_type=type(source).__name__, reason=getattr(source, "reason", None)):
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
                output = io.StringIO()
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
                    native.drive_request(request, "drive_get")
                request.execute.assert_called_once_with(num_retries=0)
                sleep.assert_not_called()
                self.assertEqual(failure.exception.code, "drive_tls_certificate_invalid")
                self.assertEqual(native.safe_error_fields(failure.exception)["ssl_reason"],
                                 "CERTIFICATE_VERIFY_FAILED")
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_legacy_ssl_args_unknown_incidental_or_conflicting_tokens_never_retry(self):
        messages = ["[SSL: FAKE_SECRET_MUST_NOT_BE_LOGGED] unknown library error",
                    "unrelated UNEXPECTED_EOF_WHILE_READING FAKE_SECRET_MUST_NOT_BE_LOGGED",
                    "EOF occurred in violation of protocolFAKE_SECRET_MUST_NOT_BE_LOGGED",
                    "[SSL: UNEXPECTED_EOF_WHILE_READING] [SSL: WRONG_VERSION_NUMBER] FAKE_SECRET_MUST_NOT_BE_LOGGED",
                    "[SSL: UNEXPECTED_EOF_WHILE_READING] [SSL: FAKE_SECRET_MUST_NOT_BE_LOGGED]"]
        sources = [ssl.SSLError(ssl.SSL_ERROR_SSL, message) for message in messages]
        conflict = ssl.SSLError(ssl.SSL_ERROR_SSL,
            "[SSL: UNEXPECTED_EOF_WHILE_READING] FAKE_SECRET_MUST_NOT_BE_LOGGED")
        conflict.reason = "WRONG_VERSION_NUMBER"
        sources.append(conflict)
        for source in sources:
            with self.subTest(message_index=sources.index(source)):
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
                output = io.StringIO()
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
                    native.drive_request(request, "drive_get")
                request.execute.assert_called_once_with(num_retries=0)
                sleep.assert_not_called()
                self.assertEqual(failure.exception.code, "drive_tls_error")
                self.assertEqual(native.safe_error_fields(failure.exception)["ssl_reason"], "unknown")
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_legacy_ssl_args_protocol_token_is_safe_but_not_retryable(self):
        for message in ("[SSL: WRONG_VERSION_NUMBER] FAKE_SECRET_MUST_NOT_BE_LOGGED",
                        b"[SSL: WRONG_VERSION_NUMBER] FAKE_SECRET_MUST_NOT_BE_LOGGED"):
            source = ssl.SSLError(ssl.SSL_ERROR_SSL, message)
            self.assertEqual(native.drive_code(source), ("drive_tls_error", False))
            details = native.safe_error_fields(source)
            self.assertEqual(details["ssl_reason"], "WRONG_VERSION_NUMBER")
            self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", json.dumps(details))

    def test_explicit_unknown_ssl_reason_cannot_fall_back_to_known_eof_token(self):
        for error_class in (ssl.SSLError, ssl.SSLEOFError):
            with self.subTest(error_class=error_class.__name__):
                source = error_class(ssl.SSL_ERROR_SSL,
                    "[SSL: UNEXPECTED_EOF_WHILE_READING] FAKE_SECRET_MUST_NOT_BE_LOGGED")
                source.reason = "FUTURE_UNKNOWN_FAKE_SECRET_MUST_NOT_BE_LOGGED"
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
                output = io.StringIO()
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
                    native.drive_request(request, "drive_get")
                request.execute.assert_called_once_with(num_retries=0)
                sleep.assert_not_called()
                self.assertEqual(failure.exception.code, "drive_tls_error")
                self.assertEqual(native.safe_error_fields(failure.exception)["ssl_reason"], "unknown")
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())
                source.args = (ssl.SSL_ERROR_SSL,
                    "[SSL: CERTIFICATE_VERIFY_FAILED] FAKE_SECRET_MUST_NOT_BE_LOGGED")
                self.assertEqual(native.drive_code(source), ("drive_tls_certificate_invalid", False))
                self.assertEqual(native.safe_error_fields(source)["ssl_reason"], "CERTIFICATE_VERIFY_FAILED")

    def test_standard_timeouts_reset_broken_pipe_and_abort_retry_same_request(self):
        errors = [TimeoutError("FAKE_SECRET_MUST_NOT_BE_LOGGED"), socket.timeout("FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                  ConnectionResetError(errno.ECONNRESET, "FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                  BrokenPipeError(errno.EPIPE, "FAKE_SECRET_MUST_NOT_BE_LOGGED"),
                  ConnectionAbortedError(errno.ECONNABORTED, "FAKE_SECRET_MUST_NOT_BE_LOGGED")]
        for error in errors:
            with self.subTest(error_type=type(error).__name__):
                request = types.SimpleNamespace(execute=mock.Mock(side_effect=[error, {"id": "same-request"}]))
                output = io.StringIO()
                with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output):
                    self.assertEqual(native.drive_request(request, "drive_create"), {"id": "same-request"})
                self.assertEqual(request.execute.call_count, 2)
                sleep.assert_called_once_with(1)
                self.assertIn("drive_connection_interrupted", output.getvalue())
                self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_exhausted_request_preserves_underlying_http_type_status_and_errno(self):
        source = self.error(404, "notFound")
        source.errno = errno.ECONNREFUSED
        request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.drive_request(request, "drive_get")
        details = native.safe_error_fields(failure.exception)
        self.assertEqual(details["error_type"], "OSError")
        self.assertEqual(details["http_status"], 404)
        self.assertEqual(details["errno"], errno.ECONNREFUSED)
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", json.dumps(details) + output.getvalue())

    def test_unknown_ssl_reason_is_redacted_and_not_retried_even_for_generic_errno(self):
        source = ssl.SSLError(ssl.SSL_ERROR_SYSCALL, "FAKE_SECRET_MUST_NOT_BE_LOGGED")
        source.reason = "FAKE_SECRET_MUST_NOT_BE_LOGGED"
        request = types.SimpleNamespace(execute=mock.Mock(side_effect=source))
        output = io.StringIO()
        with mock.patch.object(native.time, "sleep") as sleep, contextlib.redirect_stdout(output), self.assertRaises(native.NativeFailure) as failure:
            native.drive_request(request, "drive_get")
        request.execute.assert_called_once_with(num_retries=0)
        sleep.assert_not_called()
        details = native.safe_error_fields(failure.exception)
        self.assertEqual(details["error_type"], "SSLError")
        self.assertEqual(details["ssl_reason"], "unknown")
        self.assertEqual(details["errno"], int(ssl.SSL_ERROR_SYSCALL))
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", output.getvalue())

    def test_main_preserves_source_ssl_enum_and_type_across_native_wrapper(self):
        source = ssl.SSLError(ssl.SSL_ERROR_SSL, "FAKE_SECRET_MUST_NOT_BE_LOGGED")
        source.reason = "WRONG_VERSION_NUMBER"
        failure = native.NativeFailure("drive_get", "drive_tls_error", source=source)
        output, errors = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", [str(SCRIPT)]), mock.patch.object(native.os, "umask"), mock.patch.object(native, "run", side_effect=failure), contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors), self.assertRaises(SystemExit):
            native.main()
        line = errors.getvalue().strip()
        details = json.loads(line.split(" ", 2)[2])
        self.assertEqual(details["error_type"], "SSLError")
        self.assertEqual(details["ssl_reason"], "WRONG_VERSION_NUMBER")
        self.assertEqual(details["errno"], int(ssl.SSL_ERROR_SSL))
        self.assertNotIn("FAKE_SECRET_MUST_NOT_BE_LOGGED", line)


if __name__ == "__main__":
    unittest.main()
