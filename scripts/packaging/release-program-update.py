"""Replace only the reviewed Ganwen executable; BaoTa controls its service."""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import struct
import sys
import uuid

PROJECT = Path("/www/wwwroot/ganwen-go")
WORK = Path("/www/backup/ganwen-maintenance-20261013")
STATE_NAME = "program-update-state.json"
FORMAT = "ganwen-program-update-v1"
EXPECTED_CURRENT = "25a37aea40cbc42f7f33c80e277fa32676053c871ca95d405499333a02efea19"


class UpdateError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise UpdateError(code)


def digest(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def safe_path(path):
    path = Path(path)
    require(path.is_absolute() and ".." not in path.parts, "absolute_path_required")
    current = path
    while current != current.parent:
        require(not current.is_symlink(), "symlink_refused")
        current = current.parent
    require(path.resolve() == path, "path_alias_refused")
    return path


def regular(path, parent):
    path = safe_path(path)
    require(path.parent == parent and path.is_file()
            and stat.S_ISREG(path.lstat().st_mode), "regular_file_in_fixed_directory_required")
    return path


def directories():
    safe_path(PROJECT)
    safe_path(WORK)
    require(PROJECT.is_dir(), "project_directory_missing")
    WORK.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(WORK.is_dir(), "maintenance_directory_invalid")
    os.chmod(str(WORK), 0o700)


def metadata(path):
    info = Path(path).stat()
    return {"mode": stat.S_IMODE(info.st_mode), "uid": getattr(info, "st_uid", 0),
            "gid": getattr(info, "st_gid", 0)}


def www_owner():
    if os.name == "posix":
        import pwd
        user = pwd.getpwnam("www")
        return {"uid": user.pw_uid, "gid": user.pw_gid}
    # Only isolated portable tests call the functions on Windows.
    return {"uid": 0, "gid": 0}


def set_metadata(path, attributes):
    if hasattr(os, "chown"):
        os.chown(str(path), attributes["uid"], attributes["gid"])
    os.chmod(str(path), attributes["mode"])


def sync_directory(path):
    if os.name == "posix":
        descriptor = os.open(str(path), os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


def atomic_copy(source, destination, attributes, expected):
    temporary = destination.with_name(destination.name + ".program-next-" + uuid.uuid4().hex)
    try:
        with source.open("rb") as incoming, temporary.open("xb") as outgoing:
            os.chmod(str(temporary), 0o600)
            shutil.copyfileobj(incoming, outgoing)
            outgoing.flush()
            os.fsync(outgoing.fileno())
        require(digest(temporary) == expected, "copy_program_checksum")
        set_metadata(temporary, attributes)
        os.replace(str(temporary), str(destination))
        sync_directory(destination.parent)
    finally:
        if temporary.exists() and not temporary.is_symlink():
            temporary.unlink()


def save_state(state):
    temporary = WORK / (STATE_NAME + ".next-" + uuid.uuid4().hex)
    try:
        with temporary.open("x", encoding="utf-8") as stream:
            os.chmod(str(temporary), 0o600)
            json.dump(state, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(str(temporary), str(WORK / STATE_NAME))
        sync_directory(WORK)
    finally:
        if temporary.exists() and not temporary.is_symlink():
            temporary.unlink()


def fingerprints(args):
    for field in ("expected_current", "expected_new"):
        value = getattr(args, field)
        require(isinstance(value, str) and re.fullmatch(r"[0-9a-fA-F]{64}", value) is not None,
                "sha256_invalid")
        setattr(args, field, value.lower())
    require(args.expected_current != args.expected_new, "new_program_matches_current")


def elf(path):
    with path.open("rb") as stream:
        header = stream.read(64)
    require(len(header) == 64 and header[:7] == b"\x7fELF\x02\x01\x01"
            and header[7] in (0, 3), "linux_amd64_elf_required")
    kind, machine, version = struct.unpack("<HHI", header[16:24])
    require(kind in (2, 3) and machine == 62 and version == 1
            and struct.unpack("<H", header[52:54])[0] == 64, "linux_amd64_elf_required")


def assert_stopped():
    try:
        connection = socket.create_connection(("127.0.0.1", 8099), timeout=1)
    except OSError:
        connection = None
    if connection:
        connection.close()
        raise UpdateError("stop_go_project_in_baota_first")
    require(Path("/proc").is_dir(), "process_inventory_unavailable")
    for process in Path("/proc").glob("[0-9]*/exe"):
        try:
            target = os.readlink(str(process))
            if target.endswith(" (deleted)"):
                target = target[:-10]
            require(target != str(PROJECT / "ganwen"), "go_project_process_still_running")
        except (FileNotFoundError, PermissionError):
            continue


def load_state(args):
    state = json.loads(regular(WORK / STATE_NAME, WORK).read_text(encoding="utf-8"))
    require(isinstance(state, dict) and state.get("format") == FORMAT
            and state.get("project") == str(PROJECT)
            and state.get("old_sha256") == args.expected_current
            and state.get("new_sha256") == args.expected_new, "program_update_state_mismatch")
    backup = regular(WORK / ("program-before-" + args.expected_current + ".bin"), WORK)
    require(digest(backup) == args.expected_current, "previous_program_backup_changed")
    elf(backup)
    for key in ("old_metadata", "new_metadata"):
        value = state.get(key)
        require(isinstance(value, dict) and set(value) == {"uid", "gid", "mode"}
                and all(type(item) is int and item >= 0 for item in value.values())
                and value["mode"] <= 0o7777, "program_metadata_invalid")
    require(state["new_metadata"] == dict(www_owner(), mode=state["old_metadata"]["mode"]),
            "program_update_owner_changed")
    return state, backup


def verify_binary(state, old=False):
    target = regular(PROJECT / "ganwen", PROJECT)
    require(digest(target) == state["old_sha256" if old else "new_sha256"], "installed_program_changed")
    elf(target)
    require(metadata(target) == state["old_metadata" if old else "new_metadata"], "installed_program_metadata_changed")


def install(args):
    directories()
    fingerprints(args)
    state_path = WORK / STATE_NAME
    if state_path.exists() or state_path.is_symlink():
        state, backup = load_state(args)
        if state["stage"] in ("installed", "verified"):
            verify_binary(state)
            return {"stage": state["stage"], "already_installed": True}
    else:
        state, backup = None, WORK / ("program-before-" + args.expected_current + ".bin")
    require(args.binary, "new_program_input_required")
    source = Path(args.binary)
    source = source if source.is_absolute() else WORK / source
    source = regular(source, WORK)
    require(source.name not in {STATE_NAME, backup.name}, "new_program_input_conflict")
    require(digest(source) == args.expected_new, "new_program_checksum")
    elf(source)
    target = regular(PROJECT / "ganwen", PROJECT)
    elf(target)
    assert_stopped()
    current = digest(target)
    require(current in (args.expected_current, args.expected_new) if state else current == args.expected_current,
            "current_program_changed")
    if state is None:
        attributes = metadata(target)
        require(os.name != "posix" or attributes["mode"] & 0o111, "current_program_not_executable")
        if backup.exists() or backup.is_symlink():
            require(digest(regular(backup, WORK)) == args.expected_current, "previous_program_backup_changed")
        else:
            atomic_copy(target, backup, {"mode": 0o600, "uid": 0, "gid": 0}, args.expected_current)
        state = {"format": FORMAT, "project": str(PROJECT), "stage": "prepared",
                 "old_sha256": args.expected_current, "new_sha256": args.expected_new,
                 "old_metadata": attributes, "new_metadata": dict(www_owner(), mode=attributes["mode"]),
                 "created_utc": datetime.now(timezone.utc).isoformat()}
        save_state(state)
    require(state["stage"] in ("prepared", "installing", "rolled_back", "rollback_requires_attention"),
            "program_update_stage_invalid")
    state["stage"] = "installing"
    save_state(state)
    try:
        atomic_copy(source, target, state["new_metadata"], args.expected_new)
        verify_binary(state)
        state["stage"] = "installed"
        save_state(state)
    except Exception:
        try:
            assert_stopped()
            require(digest(regular(backup, WORK)) == args.expected_current, "previous_program_backup_changed")
            atomic_copy(backup, target, state["old_metadata"], args.expected_current)
            verify_binary(state, old=True)
            state["stage"] = "rolled_back"
            save_state(state)
        except Exception:
            state["stage"] = "rollback_requires_attention"
            try:
                save_state(state)
            except Exception:
                pass
            raise UpdateError("install_failed_rollback_requires_attention") from None
        raise UpdateError("install_failed_original_restored") from None
    return {"stage": "installed", "binary_sha256": args.expected_new,
            "panel_action_required": "start_go_project"}


def verify(args):
    directories()
    fingerprints(args)
    state, _ = load_state(args)
    require(state["stage"] in ("installed", "verified"), "program_update_not_installed")
    verify_binary(state)
    state["stage"] = "verified"
    save_state(state)
    return {"stage": "verified", "binary_sha256": args.expected_new}


def rollback(args):
    directories()
    fingerprints(args)
    state, backup = load_state(args)
    if state["stage"] == "rolled_back":
        verify_binary(state, old=True)
        return {"stage": "rolled_back", "already_rolled_back": True}
    require(state["stage"] in ("prepared", "installing", "installed", "verified", "rollback_requires_attention"),
            "program_update_not_recoverable")
    assert_stopped()
    target = regular(PROJECT / "ganwen", PROJECT)
    require(digest(target) in (args.expected_current, args.expected_new), "current_program_changed")
    atomic_copy(backup, target, state["old_metadata"], args.expected_current)
    verify_binary(state, old=True)
    state["stage"] = "rolled_back"
    save_state(state)
    return {"stage": "rolled_back", "binary_sha256": args.expected_current,
            "panel_action_required": "start_go_project"}


@contextmanager
def locked():
    import fcntl
    directories()
    path = WORK / ".program-update.lock"
    if path.exists() or path.is_symlink():
        regular(path, WORK)
    with path.open("a+") as stream:
        os.chmod(str(path), 0o600)
        try:
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise UpdateError("program_update_already_running") from None
        yield


def server_only():
    require(os.name == "posix" and os.geteuid() == 0, "run_on_server_as_root_via_baota_task")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("install", "verify", "rollback"))
    parser.add_argument("--binary", help="reviewed regular binary directly inside the fixed maintenance directory")
    parser.add_argument("--expected-current", default=EXPECTED_CURRENT)
    parser.add_argument("--expected-new", required=True)
    args = parser.parse_args(argv)
    try:
        server_only()
        os.umask(0o077)
        with locked():
            result = {"install": install, "verify": verify, "rollback": rollback}[args.mode](args)
        print(json.dumps(dict(result, ok=True), ensure_ascii=False))
        return 0
    except Exception as error:
        code = str(error) if isinstance(error, UpdateError) else "program_update_failed_" + type(error).__name__
        print(json.dumps({"ok": False, "mode": args.mode, "code": code}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
