"""Run one reviewed BaoTa independent-page release; service control stays in the panel.

All output is a small JSON result. Original data and configuration backups stay
private on the server. No subprocess output, credentials or article text is logged.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import sqlite3
import stat
import subprocess
import tarfile
import urllib.parse
import urllib.request
import uuid

ARCHIVE_ROOT = "ganwen-independent-page"
STATE_NAME = "independent-release-state.json"
EXPECTED_PACKAGE = "200b46028b1b235f715ad214012592b296c4b4af113b6e6a5c9bc421bab8480e"
EXPECTED_OLD_BINARY = "ea63350988fb0b43f48ff0f21ccf8f9bc40fe0d07e73c53aa502c069ceed2dc1"
MEDIA_EXTENSIONS = set("jpg jpeg png gif webp avif heic heif ico bmp swf mp4 m4v mov webm mp3 m4a ogg wav pdf txt".split())


class ReleaseError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise ReleaseError(code)


def digest(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def within(path, boundary):
    try:
        return os.path.commonpath([str(Path(path).resolve()), str(Path(boundary).resolve())]) == str(Path(boundary).resolve())
    except ValueError:
        return False


def regular(path, boundary=None):
    path = Path(path)
    require(path.is_file() and not path.is_symlink(), "regular_file_required")
    if boundary is not None:
        require(within(path, boundary), "path_outside_boundary")
        current = path.absolute()
        while current != Path(boundary).absolute().parent:
            require(not current.is_symlink(), "symlink_refused")
            current = current.parent
    return path


def metadata(path):
    info = Path(path).stat()
    return {"mode": stat.S_IMODE(info.st_mode), "uid": getattr(info, "st_uid", 0), "gid": getattr(info, "st_gid", 0)}


def set_metadata(path, attributes):
    os.chmod(path, attributes["mode"])
    if hasattr(os, "chown"):
        os.chown(path, attributes["uid"], attributes["gid"])


def media_owner(state):
    if os.name == "posix":
        import pwd
        user = pwd.getpwnam(state.get("run_user", "www"))
        return {"uid": user.pw_uid, "gid": user.pw_gid}
    # Local Windows tests never run the production entry point.
    return metadata(Path(state["project"]) / "data")


def atomic_copy(source, destination, attributes=None):
    destination = Path(destination)
    temporary = destination.with_name(destination.name + ".codex-next-" + uuid.uuid4().hex)
    require(temporary.parent.resolve() == destination.parent.resolve(), "atomic_target_invalid")
    with regular(source).open("rb") as incoming, temporary.open("xb") as outgoing:
        shutil.copyfileobj(incoming, outgoing)
        outgoing.flush()
        os.fsync(outgoing.fileno())
    set_metadata(temporary, attributes or {"mode": 0o600, "uid": os.getuid() if hasattr(os, "getuid") else 0, "gid": os.getgid() if hasattr(os, "getgid") else 0})
    require(digest(temporary) == digest(source), "atomic_copy_checksum")
    os.replace(temporary, destination)


def save_state(work, state):
    work = Path(work)
    temporary = work / (STATE_NAME + ".next")
    temporary.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.chmod(temporary, 0o600)
    os.replace(temporary, work / STATE_NAME)


def tokens(text):
    """Token positions for the restricted, reviewed Nginx location edits."""
    output, index = [], 0
    while index < len(text):
        if text[index].isspace():
            index += 1
            continue
        if text[index] == "#":
            end = text.find("\n", index)
            index = len(text) if end < 0 else end + 1
            continue
        start = index
        if text[index] in "{};":
            index += 1
        elif text[index] in "\"'":
            quote = text[index]
            index += 1
            while index < len(text):
                if text[index] == "\\":
                    index += 2
                elif text[index] == quote:
                    index += 1
                    break
                else:
                    index += 1
        else:
            while index < len(text) and not text[index].isspace() and text[index] not in "{};#":
                index += 2 if text[index] == "\\" else 1
        output.append((text[start:index], start, index))
    return output


def locations(text):
    all_tokens = tokens(text)
    output = []
    for index, token in enumerate(all_tokens):
        if token[0] != "location":
            continue
        opening = index + 1
        while opening < len(all_tokens) and all_tokens[opening][0] not in {"{", ";", "}"}:
            opening += 1
        require(opening < len(all_tokens) and all_tokens[opening][0] == "{", "nginx_location_invalid")
        depth, closing = 1, opening + 1
        while closing < len(all_tokens) and depth:
            if all_tokens[closing][0] == "{":
                depth += 1
            elif all_tokens[closing][0] == "}":
                depth -= 1
            closing += 1
        require(depth == 0, "nginx_braces_invalid")
        output.append({"header": [item[0] for item in all_tokens[index:opening]], "start": token[1],
                       "opening": all_tokens[opening][1], "end": all_tokens[closing - 1][2],
                       "body": [item[0] for item in all_tokens[opening + 1:closing - 1]]})
    return output


def patch_nginx(text, upstream="http://127.0.0.1:8099"):
    require(upstream == "http://127.0.0.1:8099", "unreviewed_upstream")
    blocks = locations(text)
    evidence = [block for block in blocks if block["header"] == ["location", "=", "/bewijs.html"]]
    media = []
    for block in blocks:
        header = block["header"]
        if len(header) == 3 and header[:2] == ["location", "~*"]:
            match = re.fullmatch(r"\\\.\(([^()]+)\)\$", header[2])
            if match and set(match.group(1).split("|")) == MEDIA_EXTENSIONS:
                media.append(block)
    old = [block for block in blocks if block["header"] == ["location", "@go_media"]]
    legacy = [block for block in blocks if block["header"] == ["location", "@legacy_media"]]
    require(len(evidence) <= 1 and len(media) == 1 and len(old) + len(legacy) == 1, "nginx_layout_unreviewed")
    edits = []
    if evidence:
        require(evidence[0]["body"] == ["try_files", "$uri", "=404", ";"], "evidence_location_unreviewed")
        edits.append((evidence[0]["start"], evidence[0]["end"], ""))
    block = media[0]
    indentation = text[text.rfind("\n", 0, block["start"]) + 1:block["start"]]
    old_body = ["try_files", "$uri", "@go_media", ";", "expires", "30d", ";"]
    new_body = ["proxy_pass", upstream, ";", "proxy_intercept_errors", "on", ";", "error_page", "404", "=", "@legacy_media", ";", "expires", "30d", ";"]
    require(block["body"] in (old_body, new_body), "media_location_unreviewed")
    if block["body"] == old_body:
        require(len(old) == 1 and old[0]["body"] == ["proxy_pass", upstream, ";"], "old_media_upstream_unreviewed")
        require(sum(item[0] == "@go_media" for item in tokens(text)) == 2, "old_media_has_other_references")
        header = text[block["start"]:block["opening"]].strip()
        replacement = header + " {\n" + indentation + "    proxy_pass " + upstream + ";\n" + indentation + "    proxy_intercept_errors on;\n" + indentation + "    error_page 404 = @legacy_media;\n" + indentation + "    expires 30d;\n" + indentation + "}"
        edits.append((block["start"], block["end"], replacement))
        fallback = "location @legacy_media {\n" + indentation + "    try_files $uri =404;\n" + indentation + "}"
        edits.append((old[0]["start"], old[0]["end"], fallback))
    else:
        require(len(legacy) == 1 and legacy[0]["body"] == ["try_files", "$uri", "=404", ";"], "legacy_fallback_unreviewed")
    for start, end, replacement in sorted(edits, reverse=True):
        text = text[:start] + replacement + text[end:]
    text = text.replace("# The independent evidence page stays exactly in the old site directory.", "# The independent evidence page is managed by the Go blog.")
    text = text.replace("# Keep the old document root for bewijs.html and existing static media.", "# Keep the old document root as a fallback for unmigrated static media.")
    text = text.replace("# Original files win. Missing originals (including the documented image\n    # alias) and all new uploads fall through to Go's safe data/media handler.", "# Blog media takes priority; actual Go 404 responses fall back to old media.")
    require(patch_locations_valid(text, upstream), "patched_nginx_invalid")
    return text


def patch_locations_valid(text, upstream):
    blocks = locations(text)
    return not any(block["header"] == ["location", "=", "/bewijs.html"] for block in blocks) and any(
        block["header"] == ["location", "@legacy_media"] and block["body"] == ["try_files", "$uri", "=404", ";"] for block in blocks) and text.count("proxy_intercept_errors on;") == 1 and "error_page 404 = @legacy_media;" in text and upstream in text


def verify_bundle(folder):
    folder = Path(folder)
    manifest_path = regular(folder / "package-manifest.json", folder)
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    expected = manifest["files"]
    fixed = {"ganwen", "README.md", "INSTALL.md", "prepared/page.json", "prepared/source.html",
             "deploy/ganwen-go.bt.conf.example", "deploy/ganwen.nl.bt.conf.example", "deploy/nginx-locations.conf.example"}
    actual = set(str(path.relative_to(folder)).replace("\\", "/") for path in folder.rglob("*") if path.is_file())
    require(actual == set(expected) | {"package-manifest.json"}, "bundle_file_set")
    require(fixed.issubset(expected) and manifest["evidence_files"] == 24 and len(expected) == 32, "bundle_contents_unreviewed")
    for relative, entry in expected.items():
        require(relative in fixed or relative.startswith("prepared/media/"), "bundle_private_file_refused")
        target = regular(folder / relative, folder)
        require(target.stat().st_size == entry["bytes"] and digest(target) == entry["sha256"], "bundle_checksum")
    prepared = json.loads((folder / "prepared/page.json").read_text(encoding="utf-8"))
    require(prepared["schema"] == 1 and len(prepared["media"]) == 24 and prepared["post"]["public_path"] == "/bewijs.html" and prepared["post"]["unlisted"] is True, "page_manifest_invalid")
    original = (folder / "prepared/source.html").read_bytes()
    require(original == prepared["post"]["source_html"].encode("utf-8") and len(original) == prepared["source"]["bytes"] and hashlib.sha256(original).hexdigest() == prepared["source"]["sha256"], "source_original_differs")
    for entry in prepared["media"]:
        relative = entry["path"]
        require(relative and not relative.startswith("/") and "\\" not in relative and ":" not in relative and all(part not in {"", ".", ".."} for part in relative.split("/")), "media_path_invalid")
        record = expected.get("prepared/media/" + relative)
        require(record == {"bytes": entry["bytes"], "sha256": entry["sha256"]}, "media_manifest_differs")
    binary = regular(folder / "ganwen", folder)
    with binary.open("rb") as stream:
        header = stream.read(20)
    require(len(header) == 20 and header[:6] == b"\x7fELF\x02\x01" and int.from_bytes(header[18:20], "little") == 62, "binary_not_linux_amd64")
    return prepared, manifest


def extract_bundle(archive_path, target):
    target = Path(target)
    require(not target.exists(), "stage_directory_exists")
    target.mkdir(mode=0o700)
    total, names = 0, set()
    with tarfile.open(archive_path, "r:gz") as archive:
        members = archive.getmembers()
        for member in members:
            require(member.name == ARCHIVE_ROOT or member.name.startswith(ARCHIVE_ROOT + "/"), "archive_root_invalid")
            require(all(part not in {"", ".", ".."} for part in member.name.split("/")), "archive_path_invalid")
            require(member.name not in names and (member.isdir() or member.isfile()), "archive_special_or_duplicate_file")
            names.add(member.name)
            require(within(target / member.name, target), "archive_path_escape")
            total += member.size
        require(0 < total < 512 * 1024 * 1024, "archive_size_invalid")
        for member in members:
            output = target / member.name
            if member.isdir():
                output.mkdir(parents=True, exist_ok=True, mode=0o700)
            else:
                output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                with archive.extractfile(member) as incoming, output.open("xb") as outgoing:
                    shutil.copyfileobj(incoming, outgoing)
                os.chmod(output, 0o750 if output.name == "ganwen" else 0o600)
    verify_bundle(target / ARCHIVE_ROOT)


def db_snapshot(database, destination):
    require(not Path(destination).exists(), "database_snapshot_exists")
    source = sqlite3.connect("file:" + urllib.parse.quote(str(Path(database).absolute()), safe="/") + "?mode=ro", uri=True)
    output = sqlite3.connect(str(destination))
    try:
        source.backup(output)
        require(output.execute("PRAGMA integrity_check").fetchone()[0] == "ok", "database_snapshot_integrity")
    finally:
        output.close()
        source.close()
    os.chmod(destination, 0o600)


def db_contract(database):
    connection = sqlite3.connect("file:" + urllib.parse.quote(str(Path(database).absolute()), safe="/") + "?mode=ro", uri=True)
    try:
        connection.execute("BEGIN")
        posts = {str(row[0]): hashlib.sha256(row[1].encode("utf-8")).hexdigest() for row in connection.execute("SELECT id,document FROM posts")}
        values = list(connection.execute("SELECT key,value FROM kv WHERE key != 'posts_revision_v1' ORDER BY key"))
        jobs = list(connection.execute("SELECT * FROM translation_jobs ORDER BY id"))
        encode = lambda rows: hashlib.sha256(json.dumps(rows, ensure_ascii=False, separators=(",", ":")).encode("utf-8")).hexdigest()
        return {"posts": posts, "values_sha256": encode(values), "jobs_sha256": encode(jobs)}
    finally:
        connection.close()


def assert_stopped(project, port=8099):
    try:
        connection = socket.create_connection(("127.0.0.1", port), timeout=1)
    except OSError:
        connection = None
    if connection:
        connection.close()
        raise ReleaseError("stop_go_project_in_baota_first")
    binary = str((Path(project) / "ganwen").resolve())
    for path in Path("/proc").glob("[0-9]*/exe"):
        try:
            target = os.readlink(path).removesuffix(" (deleted)") if hasattr(str, "removesuffix") else os.readlink(path).replace(" (deleted)", "")
            require(target != binary, "go_project_process_still_running")
        except OSError:
            continue


def run_command(arguments):
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GBLOG_")}
    result = subprocess.run([str(value) for value in arguments], env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
    require(result.returncode == 0, "maintenance_command_failed")
    return result.stdout


def runtime_sqlite(project):
    data = Path(project) / "data"
    runtime = data / "runtime.env"
    selected = {}
    if runtime.exists():
        regular(runtime, data)
        for line in runtime.read_text(encoding="utf-8").splitlines():
            key, separator, value = line.partition("=")
            if separator and key.strip() in {"GBLOG_DATABASE_DRIVER", "GBLOG_DATABASE_PATH"}:
                selected[key.strip()] = value.strip()
    require(selected.get("GBLOG_DATABASE_DRIVER", "sqlite").lower() in {"", "sqlite"}, "only_reviewed_sqlite_supported")
    configured = Path(selected.get("GBLOG_DATABASE_PATH") or "blog.db")
    configured = configured if configured.is_absolute() else data / configured
    require(configured.resolve() == (data / "blog.db").resolve(), "database_path_unreviewed")
    return regular(data / "blog.db", data)


def stage(args):
    work, project, nginx = Path(args.work_dir).absolute(), Path(args.project_dir).absolute(), Path(args.nginx_config).absolute()
    require(not within(work, project) and not within(work, "/www/wwwroot/ganwen.nl"), "maintenance_directory_public_or_inside_project")
    work.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(not work.is_symlink(), "maintenance_directory_symlink")
    os.chmod(work, 0o700)
    archive = regular(work / args.archive_name, work)
    require(digest(archive) == args.package_sha256, "uploaded_archive_checksum")
    state_path = work / STATE_NAME
    if state_path.exists():
        state = json.loads(regular(state_path, work).read_text(encoding="utf-8"))
        require(state["package_sha256"] == args.package_sha256, "different_release_state")
        verify_bundle(Path(state["bundle"]))
        return {"stage": state["stage"], "already_staged": True}
    binary = regular(project / "ganwen", project)
    require(digest(binary) == args.expected_old_binary_sha256, "current_program_changed")
    runtime_sqlite(project)
    require((project / "data/media").is_dir() and not (project / "data/media").is_symlink(), "media_root_missing_or_unsafe")
    original_nginx = regular(nginx).read_text(encoding="utf-8")
    patched = patch_nginx(original_nginx)
    extract_bundle(archive, work / "stage")
    candidate = work / "nginx.next.conf"
    candidate.write_text(patched, encoding="utf-8")
    os.chmod(candidate, 0o600)
    state = {"stage": "staged", "project": str(project), "nginx": str(nginx), "nginx_bin": args.nginx_bin,
             "bundle": str(work / "stage" / ARCHIVE_ROOT), "archive": str(archive), "package_sha256": args.package_sha256,
             "old_binary_sha256": args.expected_old_binary_sha256, "old_nginx_sha256": digest(nginx),
             "new_nginx_sha256": digest(candidate), "binary_metadata": metadata(binary), "nginx_metadata": metadata(nginx), "run_user": getattr(args, "run_user", "www")}
    media_owner(state) # Confirm the documented runtime user before any install.
    save_state(work, state)
    return {"stage": "staged", "verified_evidence_files": 24, "production_changed": False}


def load_state(work):
    work = Path(work).absolute()
    state = json.loads(regular(work / STATE_NAME, work).read_text(encoding="utf-8"))
    require(digest(regular(state["archive"], work)) == state["package_sha256"], "release_archive_changed")
    require(within(state["bundle"], work), "release_bundle_outside_work")
    verify_bundle(Path(state["bundle"]))
    return state


def preserve_before(work, state, label):
    project = Path(state["project"])
    folder = Path(work) / label
    require(not folder.exists(), "backup_directory_exists")
    folder.mkdir(mode=0o700)
    atomic_copy(project / "ganwen", folder / "ganwen.before")
    atomic_copy(state["nginx"], folder / "nginx.before.conf")
    database = runtime_sqlite(project)
    db_snapshot(database, folder / "before.sqlite")
    # These private backups are never uploaded, logged or placed in the bundle.
    for name in ("runtime.env", "translation.key", "admin-access.txt"):
        source = project / "data" / name
        if source.exists():
            atomic_copy(regular(source, project / "data"), folder / name)
    return folder


def restore_before(work, state, backup, preserve_current=True):
    project = Path(state["project"])
    assert_stopped(project)
    database = runtime_sqlite(project)
    if preserve_current:
        latest = preserve_before(work, state, "rollback-current-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:6])
    else:
        latest = Path(work) / "failed-install-current"
        latest.mkdir(mode=0o700, exist_ok=True)
    attributes = state["database_metadata"]
    for suffix in ("-wal", "-shm", "-journal"):
        sidecar = Path(str(database) + suffix)
        if sidecar.exists():
            regular(sidecar, project / "data")
            os.rename(sidecar, latest / ("database" + suffix))
    atomic_copy(Path(backup) / "before.sqlite", database, attributes)
    for name, fingerprint in state.get("private_config_sha256", {}).items():
        source = regular(Path(backup) / name, backup)
        require(digest(source) == fingerprint, "private_config_backup_checksum")
        target = project / "data" / name
        original_attributes = state.get("private_config_metadata", {}).get(name) or metadata(target)
        atomic_copy(source, target, original_attributes)
    atomic_copy(Path(backup) / "ganwen.before", project / "ganwen", state["binary_metadata"])
    atomic_copy(Path(backup) / "nginx.before.conf", state["nginx"], state["nginx_metadata"])
    # Keep complete newly installed originals readable for a later retry. A
    # partial file is retained privately by its existing root-only permissions.
    owner = media_owner(state)
    prepared, _ = verify_bundle(Path(state["bundle"]))
    expected = {entry["path"]: entry for entry in prepared["media"]}
    for relative in state.get("new_media", []):
        target = project / "data/media" / relative
        if target.is_file() and digest(target) == expected[relative]["sha256"]:
            set_metadata(regular(target, project / "data/media"), {**owner, "mode": 0o600})
    for relative in state.get("new_media_directories", []):
        target = project / "data/media" / relative
        if target.is_dir():
            require(within(target, project / "data/media") and not target.is_symlink(), "restored_media_directory_unsafe")
            set_metadata(target, {**owner, "mode": 0o700})
    require(db_contract(database) == state["before_contract"], "restored_database_contract")
    run_command([state["nginx_bin"], "-t"])


def verify_installed(work, state):
    project, folder = Path(state["project"]), Path(state["bundle"])
    prepared, manifest = verify_bundle(folder)
    require(digest(regular(project / "ganwen", project)) == manifest["files"]["ganwen"]["sha256"], "installed_program_checksum")
    require(digest(regular(state["nginx"])) == state["new_nginx_sha256"], "installed_nginx_changed")
    database = runtime_sqlite(project)
    current = db_contract(database)
    before = state["before_contract"]
    require(all(current["posts"].get(key) == value for key, value in before["posts"].items()), "existing_article_changed")
    require(current["values_sha256"] == before["values_sha256"] and current["jobs_sha256"] == before["jobs_sha256"], "settings_or_translation_jobs_changed")
    connection = sqlite3.connect("file:" + urllib.parse.quote(str(database), safe="/") + "?mode=ro", uri=True)
    try:
        evidence = []
        for row in connection.execute("SELECT status,document FROM posts WHERE kind='page'"):
            post = json.loads(row[1])
            if post.get("public_path") == "/bewijs.html":
                require(row[0] == "publish" and post.get("status") == "publish" and post.get("unlisted") is True, "evidence_visibility_invalid")
                require(post["source_html"] == prepared["post"]["source_html"] and post["content_html"] == prepared["post"]["content_html"] and post["translations"]["nl"]["content_html"] == prepared["post"]["translations"]["nl"]["content_html"], "evidence_content_changed")
                evidence.append(post)
        require(len(evidence) == 1 and len(current["posts"]) in {len(before["posts"]), len(before["posts"]) + 1}, "evidence_record_count")
    finally:
        connection.close()
    for entry in prepared["media"]:
        target = regular(project / "data/media" / entry["path"], project / "data/media")
        require(target.stat().st_size == entry["bytes"] and digest(target) == entry["sha256"], "installed_evidence_checksum")
    for name, fingerprint in state.get("private_config_sha256", {}).items():
        require(digest(project / "data" / name) == fingerprint, "private_config_changed")
    return prepared


def install(args):
    work = Path(args.work_dir).absolute()
    state = load_state(work)
    if state["stage"] in {"installed", "verified"}:
        verify_installed(work, state)
        return {"stage": state["stage"], "already_installed": True}
    require(state["stage"] == "staged", "release_state_not_staged")
    project = Path(state["project"])
    assert_stopped(project)
    require(digest(project / "ganwen") == state["old_binary_sha256"] and digest(state["nginx"]) == state["old_nginx_sha256"], "production_changed_since_stage")
    require(digest(regular(work / "nginx.next.conf", work)) == state["new_nginx_sha256"], "reviewed_nginx_candidate_changed")
    database = runtime_sqlite(project)
    backup = preserve_before(work, state, "before-install")
    state["before_contract"] = db_contract(backup / "before.sqlite")
    state["database_metadata"] = metadata(database)
    state["private_config_sha256"] = {name: digest(project / "data" / name) for name in ("runtime.env", "translation.key", "admin-access.txt") if (project / "data" / name).exists()}
    state["private_config_metadata"] = {name: metadata(project / "data" / name) for name in state["private_config_sha256"]}
    state["backup"] = str(backup)
    state["stage"] = "installing"
    save_state(work, state)
    folder = Path(state["bundle"])
    prepared, _ = verify_bundle(folder)
    new_media = [entry["path"] for entry in prepared["media"] if not (project / "data/media" / entry["path"]).exists()]
    new_directories = set()
    for relative in new_media:
        parent = (project / "data/media" / relative).parent
        while parent != project / "data/media":
            if not parent.exists():
                new_directories.add(parent)
            parent = parent.parent
    owner = media_owner(state)
    state["new_media"] = new_media
    state["new_media_directories"] = [str(directory.relative_to(project / "data/media")).replace("\\", "/") for directory in sorted(new_directories)]
    save_state(work, state)
    try:
        # The CLI exits before initializing HTTP/auth/translation workers.
        run_command([folder / "ganwen", "import-independent-page", "--database", "sqlite", "--data", project / "data", "--media", project / "data/media", "--file", folder / "prepared/page.json"])
        for directory in sorted(new_directories, key=lambda value: len(str(value))):
            set_metadata(directory, {**owner, "mode": 0o700})
        for relative in new_media:
            set_metadata(regular(project / "data/media" / relative, project / "data/media"), {**owner, "mode": 0o600})
        for suffix in ("", "-wal", "-shm", "-journal"):
            target = Path(str(database) + suffix)
            if target.exists():
                set_metadata(regular(target, project / "data"), state["database_metadata"])
        atomic_copy(folder / "ganwen", project / "ganwen", state["binary_metadata"])
        atomic_copy(work / "nginx.next.conf", state["nginx"], state["nginx_metadata"])
        run_command([state["nginx_bin"], "-t"])
        verify_installed(work, state)
    except Exception:
        try:
            restore_before(work, state, backup, preserve_current=True)
            state["stage"] = "rolled_back"
            save_state(work, state)
        except Exception:
            state["stage"] = "rollback_requires_attention"
            save_state(work, state)
            raise ReleaseError("install_failed_rollback_requires_attention") from None
        raise ReleaseError("install_failed_original_restored") from None
    state["stage"] = "installed"
    save_state(work, state)
    return {"stage": "installed", "verified_evidence_files": 24, "existing_articles_and_settings_preserved": True,
            "nginx_test": True, "panel_action_required": "start_go_project_and_reload_nginx"}


def verify(args):
    work = Path(args.work_dir).absolute()
    state = load_state(work)
    require(state["stage"] in {"installed", "verified"}, "release_not_installed")
    prepared = verify_installed(work, state)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for path in ("/bewijs.html", "/nl/bewijs.html"):
        headers = {"Host": "www.ganwen.nl"}
        if path == "/bewijs.html":
            headers["Accept-Language"] = "zh-CN"
        request = urllib.request.Request("http://127.0.0.1:8099" + path, headers=headers)
        with opener.open(request, timeout=20) as response:
            body = response.read().decode("utf-8")
            require(response.status == 200 and response.headers.get("X-Robots-Tag") == "noindex, nofollow", "runtime_evidence_visibility")
            require('data-independent-page="true"' in body and "bewijs.html" in body, "runtime_evidence_page")
    for path in ("/", "/?sitemap=posts-page-1"):
        request = urllib.request.Request("http://127.0.0.1:8099" + path, headers={"Host": "www.ganwen.nl"})
        with opener.open(request, timeout=20) as response:
            require(response.status == 200 and "/bewijs.html" not in response.read().decode("utf-8"), "runtime_evidence_discovered")
    for entry in prepared["media"]:
        media_path = "/" + urllib.parse.quote(entry["path"], safe="/")
        request = urllib.request.Request("http://127.0.0.1:8099" + media_path, method="HEAD", headers={"Host": "www.ganwen.nl"})
        with opener.open(request, timeout=20) as response:
            require(response.status == 200 and int(response.headers.get("Content-Length", -1)) == entry["bytes"], "runtime_evidence_media")
        if Path(entry["path"]).suffix.lower() in {".mp4", ".mov", ".webm"}:
            request = urllib.request.Request("http://127.0.0.1:8099" + media_path, headers={"Host": "www.ganwen.nl", "Range": "bytes=0-31"})
            with opener.open(request, timeout=20) as response:
                require(response.status == 206 and len(response.read()) == 32, "runtime_video_range")
    run_command([state["nginx_bin"], "-t"])
    state["stage"] = "verified"
    save_state(work, state)
    return {"stage": "verified", "verified_evidence_files": 24, "both_languages": True, "hidden_from_home_and_sitemap": True, "nginx_test": True}


def rollback(args):
    work = Path(args.work_dir).absolute()
    state = load_state(work)
    if state["stage"] == "rolled_back":
        return {"stage": "rolled_back", "already_rolled_back": True}
    require(state["stage"] in {"installed", "verified", "installing", "rollback_requires_attention"}, "release_not_recoverable")
    restore_before(work, state, state["backup"], preserve_current=True)
    state["stage"] = "rolled_back"
    save_state(work, state)
    return {"stage": "rolled_back", "latest_data_preserved_privately": True, "panel_action_required": "start_old_go_project_and_reload_nginx"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["stage", "install", "verify", "rollback"])
    parser.add_argument("--work-dir", default="/www/backup/ganwen-maintenance-20261013")
    parser.add_argument("--project-dir", default="/www/wwwroot/ganwen-go")
    parser.add_argument("--nginx-config", default="/www/server/panel/vhost/nginx/go_ganwen_blog.conf")
    parser.add_argument("--nginx-bin", default="/www/server/nginx/sbin/nginx")
    parser.add_argument("--run-user", default="www")
    parser.add_argument("--archive-name", default="incoming-independent-sidebar-20261013.tar.gz")
    parser.add_argument("--package-sha256", default=EXPECTED_PACKAGE)
    parser.add_argument("--expected-old-binary-sha256", default=EXPECTED_OLD_BINARY)
    args = parser.parse_args()
    try:
        require(os.name == "posix" and os.geteuid() == 0, "run_on_server_as_root_via_baota_task")
        os.umask(0o077)
        result = {"stage": stage, "install": install, "verify": verify, "rollback": rollback}[args.mode](args)
        print(json.dumps({"ok": True, **result}, ensure_ascii=False))
    except Exception as error:
        code = str(error) if isinstance(error, ReleaseError) else "maintenance_failed_" + type(error).__name__
        print(json.dumps({"ok": False, "mode": args.mode, "code": code}, ensure_ascii=False))
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
