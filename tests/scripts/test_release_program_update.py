"""Portable isolated binary-update tests; no service, database or network access."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import struct
import tempfile
import types
import unittest
from unittest import mock

SCRIPT = (Path(__file__).resolve().parents[2] / "scripts/packaging/release-program-update.py")
spec = importlib.util.spec_from_file_location("program_update_under_test", SCRIPT)
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)
SECRET = "TEST-SECRET-MUST-NOT-APPEAR"


def elf_bytes(label, machine=62):
    header = bytearray(64)
    header[:8] = b"\x7fELF\x02\x01\x01\x00"
    header[16:24] = struct.pack("<HHI", 2, machine, 1)
    header[52:54] = struct.pack("<H", 64)
    return bytes(header) + label


class ProgramUpdateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="ganwen-program-update-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.project, self.work = self.root / "project", self.root / "maintenance"
        self.project.mkdir()
        self.work.mkdir()
        self.target, self.incoming = self.project / "ganwen", self.work / "incoming.bin"
        self.old, self.new = elf_bytes(b"old program"), elf_bytes(b"new program")
        self.target.write_bytes(self.old)
        os.chmod(str(self.target), 0o751)
        self.incoming.write_bytes(self.new)
        self.args = types.SimpleNamespace(binary=str(self.incoming),
            expected_current=update.digest(self.target), expected_new=update.digest(self.incoming))
        self.old_attributes = update.metadata(self.target)
        data = self.project / "data"
        (data / "media").mkdir(parents=True)
        for name in ("blog.db", "blog.db-wal", "runtime.env", "translation.key"):
            (data / name).write_bytes(("unchanged private " + name).encode("utf-8"))
        (data / "media" / "proof.jpg").write_bytes(b"unchanged evidence")
        self.original_state = self.work / "independent-release-state.json"
        self.original_state.write_text("unchanged previous release state", encoding="utf-8")
        self.before_data = {str(path.relative_to(data)): path.read_bytes() for path in data.rglob("*") if path.is_file()}
        patches = [mock.patch.object(update, "PROJECT", self.project),
                   mock.patch.object(update, "WORK", self.work),
                   mock.patch.object(update, "assert_stopped"),
                   mock.patch.object(update, "www_owner", return_value={key: self.old_attributes[key] for key in ("uid", "gid")})]
        if hasattr(os, "chown"):
            patches.append(mock.patch.object(update.os, "chown"))
        for patcher in patches:
            patcher.start()
            self.addCleanup(patcher.stop)

    def assert_data_untouched(self):
        data = self.project / "data"
        after = {str(path.relative_to(data)): path.read_bytes() for path in data.rglob("*") if path.is_file()}
        self.assertEqual(after, self.before_data)
        self.assertEqual(self.original_state.read_text(encoding="utf-8"), "unchanged previous release state")

    def state(self):
        return json.loads((self.work / update.STATE_NAME).read_text(encoding="utf-8"))

    def test_install_verify_rollback_are_idempotent_and_only_replace_binary(self):
        self.assertEqual(update.install(self.args)["stage"], "installed")
        self.assertEqual(self.target.read_bytes(), self.new)
        self.assertEqual(update.metadata(self.target), self.old_attributes)
        self.assertTrue(update.install(self.args)["already_installed"])
        with mock.patch.object(update, "assert_stopped", side_effect=AssertionError("verify does not control service")):
            self.assertEqual(update.verify(self.args)["stage"], "verified")
            self.assertEqual(update.verify(self.args)["stage"], "verified")
        self.assertEqual(update.rollback(self.args)["stage"], "rolled_back")
        self.assertEqual(self.target.read_bytes(), self.old)
        self.assertTrue(update.rollback(self.args)["already_rolled_back"])
        self.assertEqual(update.metadata(self.target), self.old_attributes)
        self.assert_data_untouched()
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE((self.work / update.STATE_NAME).stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE((self.work / ("program-before-" + self.args.expected_current + ".bin")).stat().st_mode), 0o600)

    def test_old_or_new_hash_mismatch_refuses_mutation(self):
        for field in ("expected_current", "expected_new"):
            args = types.SimpleNamespace(**vars(self.args))
            setattr(args, field, "0" * 64)
            with self.subTest(field=field), self.assertRaises(update.UpdateError):
                update.install(args)
            self.assertEqual(self.target.read_bytes(), self.old)
            self.assertFalse((self.work / update.STATE_NAME).exists())
        self.assert_data_untouched()

    def test_input_path_outside_work_or_traversal_is_rejected(self):
        outside = self.root / "outside.bin"
        outside.write_bytes(self.new)
        for path in (str(outside), "../outside.bin", str(self.work / "subdir" / "incoming.bin")):
            args = types.SimpleNamespace(**vars(self.args))
            args.binary = path
            with self.subTest(path=path), self.assertRaises(update.UpdateError):
                update.install(args)
        self.assertEqual(self.target.read_bytes(), self.old)
        self.assertFalse((self.work / update.STATE_NAME).exists())
        self.assert_data_untouched()

    def test_wrong_architecture_and_non_elf_inputs_are_rejected(self):
        for content in (elf_bytes(b"arm64", machine=183), b"not an executable"):
            self.incoming.write_bytes(content)
            self.args.expected_new = update.digest(self.incoming)
            with self.subTest(content=content[:8]), self.assertRaisesRegex(update.UpdateError, "linux_amd64_elf_required"):
                update.install(self.args)
            self.assertEqual(self.target.read_bytes(), self.old)
        self.assert_data_untouched()

    def test_running_service_refuses_install_before_backup_or_state(self):
        with mock.patch.object(update, "assert_stopped", side_effect=update.UpdateError("stop_go_project_in_baota_first")), self.assertRaises(update.UpdateError):
            update.install(self.args)
        self.assertEqual(self.target.read_bytes(), self.old)
        self.assertFalse((self.work / update.STATE_NAME).exists())
        self.assertFalse(list(self.work.glob("program-before-*")))
        self.assert_data_untouched()

    def test_failure_after_replace_restores_original_binary_and_metadata(self):
        original_save = update.save_state
        def fail_installed(state):
            if state["stage"] == "installed":
                raise OSError(SECRET)
            return original_save(state)
        with mock.patch.object(update, "save_state", side_effect=fail_installed), self.assertRaisesRegex(update.UpdateError, "install_failed_original_restored") as failed:
            update.install(self.args)
        self.assertNotIn(SECRET, str(failed.exception))
        self.assertEqual(self.target.read_bytes(), self.old)
        self.assertEqual(update.metadata(self.target), self.old_attributes)
        self.assertEqual(self.state()["stage"], "rolled_back")
        self.assert_data_untouched()

    def test_input_change_during_atomic_copy_cannot_replace_original(self):
        original_copy = update.atomic_copy
        def altered_copy(source, destination, attributes, expected):
            if source == self.incoming:
                self.incoming.write_bytes(elf_bytes(b"unexpected input"))
            return original_copy(source, destination, attributes, expected)
        with mock.patch.object(update, "atomic_copy", side_effect=altered_copy), self.assertRaisesRegex(update.UpdateError, "install_failed_original_restored"):
            update.install(self.args)
        self.assertEqual(self.target.read_bytes(), self.old)
        self.assertFalse(list(self.project.glob("*.program-next-*")))
        self.assert_data_untouched()

    def test_rollback_refuses_unrelated_program_changes(self):
        update.install(self.args)
        unrelated = elf_bytes(b"unrelated production edit")
        self.target.write_bytes(unrelated)
        with self.assertRaisesRegex(update.UpdateError, "current_program_changed"):
            update.rollback(self.args)
        self.assertEqual(self.target.read_bytes(), unrelated)
        self.assert_data_untouched()

    def test_state_hash_mismatch_cannot_resume_or_replace_backup(self):
        update.install(self.args)
        args = types.SimpleNamespace(**vars(self.args))
        args.expected_new = "0" * 64
        with self.assertRaisesRegex(update.UpdateError, "program_update_state_mismatch"):
            update.install(args)
        self.assertEqual(self.target.read_bytes(), self.new)
        self.assert_data_untouched()

    def test_main_outputs_fixed_error_code_without_exception_contents(self):
        output = io.StringIO()
        with mock.patch.object(update, "server_only"), mock.patch.object(update.os, "umask"), \
                mock.patch.object(update, "locked", return_value=contextlib.nullcontext()), \
                mock.patch.object(update, "install", side_effect=OSError(SECRET)), contextlib.redirect_stdout(output):
            result = update.main(["install", "--binary", str(self.incoming), "--expected-new", self.args.expected_new])
        self.assertEqual(result, 1)
        self.assertEqual(json.loads(output.getvalue())["code"], "program_update_failed_OSError")
        self.assertNotIn(SECRET, output.getvalue())


if __name__ == "__main__":
    unittest.main()
