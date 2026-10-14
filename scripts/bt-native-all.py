"""Run BaoTa's existing native website backup with a Ganwen Drive folder alias."""
import argparse
import contextlib
import errno
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import re
import socket
import ssl
import stat
import sys
import time
from types import SimpleNamespace

SOURCE_SITE = "ganwen_blog"
TARGET_FOLDER = "1tNW8vhlZx5HB3ecV4fx1WxnHn26o4JX0"
WEB_ROOT = "1jWsFZUzoXgiCQlDrWuTdPgiKzg1L7uA8"
EXPECTED_OWNER = "ganwen33@gmail.com"
DATABASE_ROOT = "1D6RQFgEyeIoVuplqZltXfefBb1fWyJiD"
MYSQL_ROOT = "1J7BTUaefIPGZvAXlhS_POVAN5-uAu5Z8"
DRIVE_ATTEMPTS = 7
MIN_DRIVE_INTERVAL = 1.1
_last_drive_request = 0.0


class NativeFailure(RuntimeError):
    def __init__(self, phase, code, source=None, summary=None):
        super().__init__(code)
        self.phase, self.code = phase, code
        self.diagnostics = safe_error_fields(source) if source is not None else None
        self.summary = summary or {}


SSL_REASONS = {"CERTIFICATE_VERIFY_FAILED", "DECRYPTION_FAILED_OR_BAD_RECORD_MAC",
               "SSLV3_ALERT_BAD_RECORD_MAC", "UNEXPECTED_EOF_WHILE_READING",
               "EOF_OCCURRED_IN_VIOLATION_OF_PROTOCOL", "TLSV1_ALERT_INTERNAL_ERROR",
               "WRONG_VERSION_NUMBER", "UNSUPPORTED_PROTOCOL", "NO_SHARED_CIPHER",
               "TLSV1_ALERT_PROTOCOL_VERSION", "SSLV3_ALERT_HANDSHAKE_FAILURE"}


def standard_ssl_reason(error):
    """Python 3.7 wrappers may keep the standard SSL token only in args[1]."""
    if not isinstance(error, ssl.SSLError):
        return None
    reason = getattr(error, "reason", None)
    arguments = getattr(error, "args", ())
    message = arguments[1] if len(arguments) > 1 else ""
    if isinstance(message, bytes):
        message = message.decode("utf-8", errors="replace")
    if not isinstance(message, str):
        message = ""
    # No message, URL or arbitrary token is returned or logged. Certificate
    # failures take priority even if a wrapper also carries an EOF token.
    tokens = set(re.findall(r"\[SSL:\s*([A-Z][A-Z0-9_]{0,79})\]", message))
    if (isinstance(error, ssl.SSLCertVerificationError)
            or reason == "CERTIFICATE_VERIFY_FAILED"
            or getattr(error, "verify_code", None) is not None
            or re.search(r"\bCERTIFICATE_VERIFY_FAILED\b", message)):
        return "CERTIFICATE_VERIFY_FAILED"
    if isinstance(reason, str) and reason in SSL_REASONS:
        return reason if not tokens or tokens == {reason} else "unknown"
    if reason is not None and reason != "":
        return "unknown"
    if len(tokens) == 1:
        token = next(iter(tokens))
        return token if token in SSL_REASONS else "unknown"
    if tokens:
        return "unknown"
    if (isinstance(error, ssl.SSLEOFError)
            or re.match(r"\s*EOF occurred in violation of protocol(?:\s|$)", message,
                        flags=re.IGNORECASE)):
        return "EOF_OCCURRED_IN_VIOLATION_OF_PROTOCOL"
    return "unknown"


def safe_error_fields(error):
    if isinstance(error, NativeFailure):
        if error.diagnostics is not None:
            return dict(error.diagnostics)
        if isinstance(error.__cause__, Exception) and error.__cause__ is not error:
            return safe_error_fields(error.__cause__)
    name = type(error).__name__
    name = name if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,79}", name) else "UnknownException"
    status = getattr(getattr(error, "resp", None), "status", None)
    try:
        status = int(status) if status is not None else None
    except (ValueError, TypeError):
        status = None
    if status is not None and not 100 <= status <= 599:
        status = None
    number = getattr(error, "errno", None)
    return {"error_type": name, "http_status": status,
            "ssl_reason": standard_ssl_reason(error),
            "errno": int(number) if isinstance(number, int) and not isinstance(number, bool) else None}


def drive_code(error):
    """Read only standard classification fields; never log response text or URLs."""
    if isinstance(error, ssl.SSLCertVerificationError):
        return "drive_tls_certificate_invalid", False
    if isinstance(error, ssl.SSLError):
        reason = standard_ssl_reason(error)
        if reason == "CERTIFICATE_VERIFY_FAILED" or getattr(error, "verify_code", None) is not None:
            return "drive_tls_certificate_invalid", False
        if reason == "unknown" and getattr(error, "reason", None) not in (None, ""):
            return "drive_tls_error", False
        transient = {"DECRYPTION_FAILED_OR_BAD_RECORD_MAC", "SSLV3_ALERT_BAD_RECORD_MAC",
                     "UNEXPECTED_EOF_WHILE_READING", "EOF_OCCURRED_IN_VIOLATION_OF_PROTOCOL",
                     "TLSV1_ALERT_INTERNAL_ERROR"}
        if isinstance(error, (ssl.SSLEOFError, ssl.SSLZeroReturnError)) or (isinstance(reason, str) and reason in transient):
            return "drive_tls_connection_interrupted", True
        return "drive_tls_error", False
    if (isinstance(error, (TimeoutError, socket.timeout, ConnectionResetError,
                           ConnectionAbortedError, BrokenPipeError))
            or (isinstance(error, OSError) and getattr(error, "errno", None) in
                {errno.ETIMEDOUT, errno.ECONNRESET, errno.ECONNABORTED, errno.EPIPE})):
        return "drive_connection_interrupted", True
    try:
        status = int(getattr(getattr(error, "resp", None), "status", 0))
    except (TypeError, ValueError):
        status = 0
    reasons, message = set(), ""
    try:
        body = json.loads(getattr(error, "content", b"{}"))
        detail = body.get("error", {})
        reasons = {item.get("reason") for item in detail.get("errors", []) if isinstance(item, dict)}
        message = str(detail.get("message", "")).lower()
    except (TypeError, ValueError, AttributeError):
        pass
    if "storageQuotaExceeded" in reasons:
        return "drive_storage_quota_exceeded", False
    minute_quota = (status == 403 and ("quotaExceeded" in reasons or "quota exceeded for quota metric" in message)
                    and any(word in message for word in ("per minute", "per-minute", "per second")))
    if status == 429 or reasons.intersection({"rateLimitExceeded", "userRateLimitExceeded"}) or minute_quota:
        return "drive_rate_limited", True
    if status in (500, 502, 503, 504):
        return "drive_temporarily_unavailable", True
    if status == 401:
        return "drive_authentication_failed", False
    if status == 403:
        return "drive_access_or_quota_denied", False
    return "drive_request_failed", False


def drive_request(request, phase):
    # Preserve the same HttpRequest and resumable URI across retries. Calling
    # upload_file again could otherwise start a second cloud object.
    for attempt in range(DRIVE_ATTEMPTS):
        try:
            global _last_drive_request
            delay = max(0, MIN_DRIVE_INTERVAL - (time.monotonic() - _last_drive_request))
            if delay:
                time.sleep(delay)
            _last_drive_request = time.monotonic()
            return request.execute(num_retries=0)
        except Exception as error:
            code, retryable = drive_code(error)
            if not retryable or attempt == DRIVE_ATTEMPTS - 1:
                details = dict(safe_error_fields(error), phase=phase, code=code)
                print("NATIVE_DRIVE_FAILED " + json.dumps(details), flush=True)
                raise NativeFailure(phase, code, source=error) from error
            delay = min(32, 2 ** attempt)
            print("NATIVE_DRIVE_RETRY " + json.dumps(dict(safe_error_fields(error), phase=phase,
                code=code, attempt=attempt + 1, delay_seconds=delay)), flush=True)
            time.sleep(delay)


class RetryingRequest:
    def __init__(self, request, phase):
        self.request, self.phase = request, phase

    def execute(self, **_):
        return drive_request(self.request, self.phase)

    def __getattr__(self, name):
        return getattr(self.request, name)


class RetryingFiles:
    def __init__(self, files):
        self.files = files

    def __getattr__(self, name):
        original = getattr(self.files, name)
        if not callable(original):
            return original
        def request(*args, **kwargs):
            return RetryingRequest(original(*args, **kwargs), "drive_" + name)
        return request


class RetryingDrive:
    def __init__(self, service):
        self.service = service

    def files(self):
        return RetryingFiles(self.service.files())

    def __getattr__(self, name):
        return getattr(self.service, name)


@contextlib.contextmanager
def official_drive_retries(client):
    # The installed official plugin calls its module's build() for each upload.
    # Adapt only this process; no plugin file or credentials are rewritten.
    module = sys.modules.get(type(client).__module__)
    original = getattr(module, "build", None)
    if not callable(original):
        yield
        return
    def build_with_retries(*args, **kwargs):
        return RetryingDrive(original(*args, **kwargs))
    module.build = build_with_retries
    try:
        yield
    finally:
        module.build = original


def field(record, name, default=None):
    return next((value for key, value in record.items() if key.lower() == name.lower()), default)


def load_runtime(panel):
    os.chdir(str(panel))
    sys.path.insert(0, str(panel / "class"))
    sys.path.insert(0, str(panel / "plugin/gdrive"))
    import public
    import panelBackup
    from CloudStoraUpload import CloudStoraUpload
    from googleapiclient.discovery import build
    # Reuse the installed plugin's current credential object and original scopes.
    # Its authentication diagnostics must never reach a cron log.
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
        storage = CloudStoraUpload()
        client = storage.run("gdrive")
    if not client:
        raise RuntimeError("Existing Google Drive authentication unavailable")
    credentials = getattr(client, "_gdrive_main__creds", None)
    if credentials is None:
        raise RuntimeError("Existing Google Drive authentication unavailable")
    service = RetryingDrive(build("drive", "v3", credentials=credentials, cache_discovery=False))
    return SimpleNamespace(public=public, backup_class=panelBackup.backup,
                           storage=storage, storage_class=CloudStoraUpload,
                           client=client, service=service)


def cron_template(public, echo, save, kind="site"):
    fields = "echo,sType,sName,backupto,save,save_local,split_type,db_type"
    if echo:
        records = public.M("crontab").where("echo=?", (echo,)).field(fields).select()
    else:
        records = public.M("crontab").where("sType=? AND sName=?", (kind, "ALL")).field(fields).select()
    if not isinstance(records, list):
        raise NativeFailure("configuration", "native_template_query_failed")
    candidates = [row for row in records
                  if isinstance(row, dict) and field(row, "sType") == kind
                  and field(row, "sName") == "ALL" and field(row, "backupto") == "gdrive"]
    if len(candidates) != 1:
        raise NativeFailure("configuration", "native_template_missing_or_ambiguous")
    record = candidates[0]
    if (not field(record, "echo") or int(field(record, "save", -1)) != save
            or str(field(record, "save_local", "")) != "0"
            or (kind == "site" and str(field(record, "split_type", "")) != "0")
            or (kind == "database" and (str(field(record, "split_type", "")) not in ("0", "", "null", "None")
                                         or str(field(record, "db_type", "")).lower() != "mysql"))):
        raise NativeFailure("configuration", "native_template_options_invalid")
    return field(record, "echo")


def verified_folder(service):
    meta = service.files().get(fileId=TARGET_FOLDER,
        fields="id,name,mimeType,parents,trashed,shared,owners(emailAddress),capabilities(canAddChildren)").execute(num_retries=3)
    if (meta.get("id") != TARGET_FOLDER or meta.get("name") != "ganwen.nl"
            or meta.get("mimeType") != "application/vnd.google-apps.folder"
            or meta.get("parents") != [WEB_ROOT] or meta.get("trashed")
            or meta.get("shared") is not False
            or [owner.get("emailAddress") for owner in meta.get("owners", [])] != [EXPECTED_OWNER]
            or not meta.get("capabilities", {}).get("canAddChildren")):
        raise NativeFailure("site_folder", "ganwen_folder_identity_failed")
    return meta["id"]


def verified_database_folders(service, names, create=False):
    fields = "id,name,mimeType,parents,trashed,shared,owners(emailAddress),capabilities(canAddChildren)"
    def valid(meta, name, parent):
        return (isinstance(meta, dict) and bool(meta.get("id")) and meta.get("name") == name
                and meta.get("mimeType") == "application/vnd.google-apps.folder"
                and meta.get("parents") == [parent] and meta.get("trashed") is False
                and meta.get("shared") is False
                and [owner.get("emailAddress") for owner in meta.get("owners", [])] == [EXPECTED_OWNER]
                and meta.get("capabilities", {}).get("canAddChildren") is True)
    root = service.files().get(fileId=DATABASE_ROOT, fields=fields).execute(num_retries=0)
    if root.get("id") != DATABASE_ROOT or not valid(root, "crontab_backup", MYSQL_ROOT):
        raise NativeFailure("database_folder", "database_root_identity_failed")
    aliases = {}
    for name in names:
        # Historic language SQL files share the web/language folder. Always
        # preserve that destination; creating a new db/language breaks restore.
        parent = WEB_ROOT if name == "language" else DATABASE_ROOT
        escaped = name.replace("\\", "\\\\").replace("'", "\\'")
        query = "'{}' in parents and name='{}' and mimeType='application/vnd.google-apps.folder' and trashed=false".format(parent, escaped)
        found = service.files().list(q=query, fields="files(" + fields + "),nextPageToken", pageSize=100).execute(num_retries=0)
        entries = found.get("files", [])
        if found.get("nextPageToken") or len(entries) > 1 or (entries and not valid(entries[0], name, parent)):
            raise NativeFailure("database_folder", "database_folder_identity_failed")
        if not entries:
            if name == "language":
                raise NativeFailure("database_folder", "language_legacy_folder_missing")
            if not create:
                aliases[name] = None
                continue
            created = service.files().create(body={"name": name, "mimeType": "application/vnd.google-apps.folder", "parents": [parent]}, fields="id").execute(num_retries=0)
            meta = service.files().get(fileId=created["id"], fields=fields).execute(num_retries=0)
            if not valid(meta, name, parent):
                raise NativeFailure("database_folder", "created_database_folder_identity_failed")
            entries = [meta]
        aliases[name] = entries[0]["id"]
    return aliases


def registered_sites(public):
    records = public.M("sites").field("name").select()
    if (not isinstance(records, list) or not records
            or any(not isinstance(row, dict) or not isinstance(row.get("name"), str)
                   or not row["name"] or row["name"] in (".", "..")
                   or "/" in row["name"] or "\\" in row["name"] for row in records)):
        raise NativeFailure("configuration", "native_site_names_invalid")
    names = [row["name"] for row in records]
    if len(names) != len(set(names)):
        raise NativeFailure("configuration", "native_site_names_ambiguous")
    return set(names)


def real_path(path):
    path = Path(path)
    if not path.is_absolute() or ".." in path.parts or path.resolve() != path:
        raise NativeFailure("archive_path", "native_archive_path_invalid")
    current = path
    while current != current.parent:
        if current.is_symlink():
            raise NativeFailure("archive_path", "native_archive_symlink_refused")
        current = current.parent
    return path


def archive_site(path, public, names):
    path = real_path(path)
    get_path = getattr(public, "get_backup_path", None)
    if not callable(get_path):
        raise NativeFailure("archive_path", "native_backup_directory_unavailable")
    root = real_path(Path(get_path()) / "site")
    if not path.is_file() or not stat.S_ISREG(path.lstat().st_mode):
        raise NativeFailure("archive_path", "native_archive_regular_file_required")
    site_dir = path.parent.parent if path.parent.name == ".failed-uploads" else path.parent
    site = site_dir.name
    if (site_dir.parent != root or site not in names
            or re.fullmatch(r"web_" + re.escape(site) + r"_\d{8}_\d{6}_[A-Za-z0-9]+\.tar\.gz", path.name) is None):
        raise NativeFailure("archive_path", "native_archive_site_or_name_invalid")
    return path, site


def retain_failed_archive(raw, public, names, site):
    path, matched = archive_site(raw, public, names)
    if matched != site:
        raise NativeFailure("archive_retention", "failed_archive_site_mismatch")
    if path.parent.name == ".failed-uploads":
        os.chmod(str(path.parent), 0o700)
        os.chmod(str(path), 0o600)
        return path
    directory = real_path(path.parent / ".failed-uploads")
    directory.mkdir(mode=0o700, exist_ok=True)
    if not directory.is_dir() or directory.is_symlink():
        raise NativeFailure("archive_retention", "failed_archive_directory_invalid")
    os.chmod(str(directory), 0o700)
    target = real_path(directory / path.name)
    if target.exists() or target.is_symlink():
        if not target.is_file() or target.is_symlink() or not os.path.samefile(str(path), str(target)):
            raise NativeFailure("archive_retention", "failed_archive_collision")
    else:
        try:
            os.link(str(path), str(target))
        except OSError as error:
            raise NativeFailure("archive_retention", "failed_archive_hardlink_failed", source=error) from error
    os.chmod(str(target), 0o600)
    if not os.path.samefile(str(path), str(target)):
        raise NativeFailure("archive_retention", "failed_archive_hardlink_unverified")
    return target


def verified_site_folder(service, site):
    if site == SOURCE_SITE:
        return verified_folder(service)
    escaped = site.replace("\\", "\\\\").replace("'", "\\'")
    fields = "id,name,mimeType,parents,trashed,shared,owners(emailAddress),capabilities(canAddChildren)"
    query = "'{}' in parents and name='{}' and mimeType='application/vnd.google-apps.folder' and trashed=false".format(WEB_ROOT, escaped)
    response = service.files().list(q=query, fields="nextPageToken,files(" + fields + ")", pageSize=100).execute(num_retries=0)
    entries = response.get("files", [])
    if response.get("nextPageToken") or len(entries) != 1:
        raise NativeFailure("site_folder", "native_site_folder_missing_or_ambiguous")
    meta = entries[0]
    if (not meta.get("id") or meta.get("name") != site or meta.get("parents") != [WEB_ROOT]
            or meta.get("mimeType") != "application/vnd.google-apps.folder"
            or meta.get("trashed") is not False or meta.get("shared") is not False
            or [owner.get("emailAddress") for owner in meta.get("owners", [])] != [EXPECTED_OWNER]
            or meta.get("capabilities", {}).get("canAddChildren") is not True):
        raise NativeFailure("site_folder", "native_site_folder_identity_failed")
    return meta["id"]


def verified_archive_object(service, path, folder_id):
    query = "'{}' in parents and name='{}' and trashed=false".format(folder_id, path.name.replace("'", "\\'"))
    response = service.files().list(q=query, fields="nextPageToken,files(id)", pageSize=100).execute(num_retries=0)
    matches = response.get("files", [])
    if response.get("nextPageToken") or len(matches) > 1:
        raise NativeFailure("retry_archive", "native_archive_cloud_ambiguous")
    if not matches:
        return False
    file_id = matches[0].get("id")
    if not file_id:
        raise NativeFailure("retry_archive", "native_archive_cloud_id_missing")
    meta = service.files().get(fileId=file_id,
        fields="id,name,size,md5Checksum,parents,trashed,shared,owners(emailAddress)").execute(num_retries=0)
    checksum = hashlib.md5()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(block)
    if (meta.get("id") != file_id or meta.get("name") != path.name
            or int(meta.get("size", "-1")) != path.stat().st_size
            or meta.get("md5Checksum", "").lower() != checksum.hexdigest()
            or meta.get("parents") != [folder_id] or meta.get("trashed") is not False
            or meta.get("shared") is not False
            or [owner.get("emailAddress") for owner in meta.get("owners", [])] != [EXPECTED_OWNER]):
        raise NativeFailure("retry_archive", "native_archive_cloud_metadata_mismatch")
    print("NATIVE_ARCHIVE_REUSED " + json.dumps({"site": path.parent.parent.name if path.parent.name == ".failed-uploads" else path.parent.name,
        "id": file_id, "parent": folder_id, "size": path.stat().st_size}), flush=True)
    return True


@contextlib.contextmanager
def reuse_official_storage(runtime):
    original = runtime.storage_class.run
    def existing(storage, name):
        if name == "gdrive":
            storage.obj = runtime.client
            storage.backup_path = runtime.storage.backup_path
            return runtime.client
        return original(storage, name)
    runtime.storage_class.run = existing
    try:
        yield
    finally:
        runtime.storage_class.run = original


@contextlib.contextmanager
def native_hooks(tool, client, service, target, kind="site", database_aliases=None,
                 public=None, names=None, site_aliases=None):
    original_folder = client._create_folder_cycle
    original_upload = client.upload_file
    original_site = tool.backup_site if kind == "site" else tool.backup_database
    original_cleanup = tool.delete_old
    state = {"site": None, "hits": 0, "results": [], "parent": None, "failures": {}}

    def resolve_folder(logical_path):
        if kind == "site" and site_aliases is not None:
            parts = [part for part in str(logical_path).split("/") if part]
            if parts != ["bt_backup", "web", state["site"]] or state["site"] not in site_aliases:
                raise NativeFailure("site_folder", "unexpected_site_upload_path")
            state["hits"] += 1
            state["parent"] = site_aliases[state["site"]]
            return state["parent"]
        if kind == "database":
            parts = [part for part in str(logical_path).split("/") if part]
            if parts != ["bt_backup", "database", "mysql", state["site"]] or state["site"] not in database_aliases:
                raise NativeFailure("database_folder", "unexpected_database_upload_path")
            state["hits"] += 1
            state["parent"] = database_aliases[state["site"]]
            return state["parent"]
        if state["site"] == SOURCE_SITE:
            parts = [part for part in str(logical_path).split("/") if part]
            if parts != ["bt_backup", "web", SOURCE_SITE]:
                raise RuntimeError("Unexpected Ganwen upload path; refusing to create another folder")
            state["hits"] += 1
            state["parent"] = target
            return target
        parent = original_folder(logical_path)
        state["parent"] = parent
        return parent

    def upload_verified(get=None, data_type=None):
        path = Path(get if isinstance(get, str) else get.filename)
        size, checksum = path.stat().st_size, hashlib.md5()
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                checksum.update(block)
        state["parent"] = None
        output = io.StringIO()
        try:
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
                if kind == "database":
                    # PanelBackup passes a full object path as data_type, which the
                    # installed cloud adapter would classify as "other". Its
                    # supported object form preserves the official uploader while
                    # explicitly selecting the verified historic SQL directory.
                    upload = SimpleNamespace(filename=str(path), filepath="bt_backup/database/mysql/" + state["site"] + "/")
                    result = original_upload(upload, None)
                else:
                    result = original_upload(get, data_type)
        finally:
            # Only our fixed-code retry diagnostics are safe to replay from the
            # otherwise suppressed official plugin output.
            for line in output.getvalue().splitlines():
                if line.startswith(("NATIVE_DRIVE_RETRY ", "NATIVE_DRIVE_FAILED ")):
                    print(line, flush=True)
        ids = re.findall(r"Upload Success\s*,\s*File ID:\s*([A-Za-z0-9_-]+)", output.getvalue())
        if result is not True or len(ids) != 1 or ids[0] == "None" or not state["parent"]:
            raise NativeFailure("upload", "native_upload_completion_unverified")
        meta = service.files().get(fileId=ids[0],
            fields="id,name,size,md5Checksum,parents,trashed,shared,owners(emailAddress)").execute(num_retries=3)
        if (meta.get("id") != ids[0] or meta.get("name") != path.name
                or meta.get("trashed") or meta.get("shared") is not False
                or meta.get("parents") != [state["parent"]]
                or int(meta.get("size", "-1")) != size
                or meta.get("md5Checksum", "").lower() != checksum.hexdigest()
                or [owner.get("emailAddress") for owner in meta.get("owners", [])] != [EXPECTED_OWNER]):
            raise NativeFailure("upload_readback", "native_upload_metadata_mismatch")
        print("NATIVE_UPLOAD_VERIFIED " + json.dumps({"site": state["site"],
            "id": ids[0], "parent": state["parent"], "size": size}), flush=True)
        return True

    def upload_checked(get=None, data_type=None):
        try:
            return upload_verified(get, data_type)
        except Exception as error:
            details = dict(safe_error_fields(error), site=state["site"], kind=kind,
                phase=getattr(error, "phase", "upload"), code=getattr(error, "code", drive_code(error)[0]))
            if kind == "site":
                details["archive_retained"] = False
                try:
                    raw = get if isinstance(get, str) else get.filename
                    retained = retain_failed_archive(raw, public, names, state["site"])
                    details.update(archive_retained=True, archive=str(retained))
                except Exception as retention_error:
                    details["retention_code"] = getattr(retention_error, "code", "failed_archive_retention_failed")
                    print("NATIVE_ARCHIVE_RETAIN_FAILED " + json.dumps(dict(safe_error_fields(retention_error),
                        site=state["site"], code=details["retention_code"])), flush=True)
            state["failures"][state["site"]] = details
            print("NATIVE_UPLOAD_FAILED " + json.dumps(details), flush=True)
            # The verified hardlink survives native save_local=0 cleanup; normal
            # successful uploads still follow the original cleanup behavior.
            return False

    def observe_site(site, *args, **kwargs):
        previous, hits = state["site"], state["hits"]
        state["site"] = site
        try:
            result = original_site(site, *args, **kwargs)
            successful = ((isinstance(result, str) and bool(result)) if kind == "site" else
                          (isinstance(result, tuple) and len(result) == 2 and result[0] is True and isinstance(result[1], str) and bool(result[1])))
            if (site == SOURCE_SITE or kind == "database") and successful and state["hits"] == hits:
                raise RuntimeError("Ganwen upload did not use the verified folder alias")
            row = {"site": site, "success": successful}
            if not successful and site in state["failures"]:
                row["failure"] = state["failures"][site]
            state["results"].append(row)
            return result if successful else (False, "Native database backup failed") if kind == "database" else False
        except Exception as error:
            details = dict(safe_error_fields(error), site=site, kind=kind,
                phase=getattr(error, "phase", "native_backup"), code=getattr(error, "code", "native_target_failed"))
            tool._error_msg = "Native " + kind + " backup failed: " + details["error_type"]
            state["results"].append({"site": site, "success": False, "failure": details})
            print("NATIVE_SITE_FAILED " + json.dumps(details), flush=True)
            return (False, tool._error_msg) if kind == "database" else False
        finally:
            state["site"] = previous

    def cleanup_local_records(backups, save, data_type=None, site_name=None):
        if data_type != kind:
            raise RuntimeError("Unexpected backup cleanup type")
        # The separate verified offsite runner enforces remote retention using
        # recoverable Trash. Never invoke the plugin's permanent cloud deletion.
        cloud = tool._cloud
        tool._cloud = None
        try:
            return original_cleanup(backups, save, data_type, site_name)
        finally:
            tool._cloud = cloud

    client._create_folder_cycle = resolve_folder
    client.upload_file = upload_checked
    if kind == "site":
        tool.backup_site = observe_site
    else:
        tool.backup_database = observe_site
    tool.delete_old = cleanup_local_records
    try:
        with official_drive_retries(client):
            yield state
    finally:
        client._create_folder_cycle = original_folder
        client.upload_file = original_upload
        if kind == "site":
            tool.backup_site = original_site
        else:
            tool.backup_database = original_site
        tool.delete_old = original_cleanup


def execute_retry_archives(args, runtime, echo, names):
    archives = [archive_site(raw, runtime.public, names) for raw in args.retry_archives]
    targets = [site for _, site in archives]
    if len(targets) != len(set(targets)):
        raise NativeFailure("retry_archive", "retry_archive_site_duplicated")
    aliases = {site: verified_site_folder(runtime.service, site) for site in targets}
    tool = runtime.backup_class(cloud_object=runtime.client, cron_info={"echo": echo})
    tool._is_save_local = False
    results = []
    with native_hooks(tool, runtime.client, runtime.service, None, public=runtime.public,
                      names=names, site_aliases=aliases) as state:
        for path, site in archives:
            state["site"] = site
            try:
                reused = verified_archive_object(runtime.service, path, aliases[site])
                if not reused and runtime.client.upload_file(str(path), "site") is not True:
                    failure = state["failures"].get(site)
                    if failure:
                        results.append({"site": site, "success": False, "failure": failure})
                        continue
                    raise NativeFailure("retry_archive", "native_archive_upload_failed")
                results.append({"site": site, "success": True})
            except Exception as error:
                details = dict(safe_error_fields(error), site=site, phase=getattr(error, "phase", "retry_archive"),
                    code=getattr(error, "code", drive_code(error)[0]), archive_retained=path.is_file())
                print("NATIVE_ARCHIVE_RETRY_FAILED " + json.dumps(details), flush=True)
                results.append({"site": site, "success": False, "failure": details})
    failed = [item for item in results if not item["success"]]
    if failed:
        raise NativeFailure("retry_archive", "one_or_more_archive_retries_failed", summary={
            "scope": "archive_retry", "targets": targets,
            "successful_targets": [item["site"] for item in results if item["success"]],
            "failed_targets": [item["failure"] for item in failed]})
    print("NATIVE_RETRY_SUCCESS " + json.dumps({"scope": "archive_retry", "targets": targets,
        "count": len(results), "archives_retained": True}), flush=True)


def execute(args, runtime):
    kind = getattr(args, "kind", "site")
    echo = cron_template(runtime.public, args.echo, args.save, kind)
    target, database_aliases, names = None, None, None
    if kind == "site":
        names = registered_sites(runtime.public)
        if args.site != "ALL" and args.site not in names:
            raise NativeFailure("configuration", "native_site_not_registered")
        target = verified_folder(runtime.service)
    else:
        records = runtime.public.M("databases").where("LOWER(type)=LOWER(?)", ("mysql",)).field("name").select()
        if not isinstance(records, list) or not records or any(not isinstance(record, dict) or not isinstance(record.get("name"), str) or not record["name"] for record in records):
            raise NativeFailure("configuration", "native_mysql_databases_missing")
        names = [record["name"] for record in records]
        if len(names) != len(set(names)):
            raise NativeFailure("configuration", "native_mysql_database_names_ambiguous")
        database_aliases = verified_database_folders(runtime.service, names, create=not args.check_only)
    if args.check_only:
        print("NATIVE_CHECK_SUCCESS " + json.dumps({"kind": kind, "echo": echo, "folder": target,
            "database_folders": database_aliases, "save": args.save}), flush=True)
        return
    if getattr(args, "retry_archives", None):
        execute_retry_archives(args, runtime, echo, names)
        return
    tool = runtime.backup_class(cloud_object=runtime.client, cron_info={"echo": echo})
    tool._is_save_local = False
    with native_hooks(tool, runtime.client, runtime.service, target, kind, database_aliases,
                      public=runtime.public, names=names) as state:
        if kind == "database":
            with reuse_official_storage(runtime):
                tool.backup_database_all(save=args.save, echo_id=echo)
        elif args.site == "ALL":
            tool.backup_site_all(save=args.save, echo_id=echo)
        else:
            result = tool.backup_site(args.site, save=args.save, echo_id=echo)
            tool.save_backup_status(bool(result), target=args.site + "|site", msg=tool._error_msg)
        if not state["results"] or any(not item["success"] for item in state["results"]):
            raise NativeFailure("native_backup", "one_or_more_native_targets_failed", summary={
                "scope": kind, "successful_targets": [item["site"] for item in state["results"] if item["success"]],
                "failed_targets": [item.get("failure", {"site": item["site"], "code": "native_target_failed"})
                                   for item in state["results"] if not item["success"]]})
        if kind == "site" and args.site == SOURCE_SITE and not any(item["site"] == SOURCE_SITE for item in state["results"]):
            raise RuntimeError("The requested native website was not backed up")
        if kind == "database" and {item["site"] for item in state["results"]} != set(database_aliases):
            raise NativeFailure("database_backup", "native_mysql_coverage_incomplete")
        if kind == "site" and {item["site"] for item in state["results"]} != (names if args.site == "ALL" else {args.site}):
            raise NativeFailure("native_backup", "native_site_coverage_incomplete")
        summary = {"kind": kind, "sites" if kind == "site" else "databases": [item["site"] for item in state["results"]],
                   "count": len(state["results"]), "save": args.save, "ganwen_folder": target}
    print("NATIVE_BACKUP_SUCCESS " + json.dumps(summary), flush=True)


def run(args):
    if args.check_only:
        execute(args, load_runtime(Path(args.panel)))
        return
    directory = Path(args.lock_dir).absolute()
    if directory.is_symlink():
        raise RuntimeError("Native backup lock directory must not be a symlink")
    directory.mkdir(parents=True, exist_ok=True)
    if directory.resolve() != directory:
        raise RuntimeError("Native backup lock directory must not traverse symlinks")
    os.chmod(str(directory), 0o700)
    with (directory / ".native-site-backup.lock").open("a+") as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("A native website backup is already running")
        execute(args, load_runtime(Path(args.panel)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--panel", default="/www/server/panel")
    parser.add_argument("--lock-dir", default="/www/backup/project-supplement")
    parser.add_argument("--echo", help="Original native ALL task echo for the selected kind; auto-detected when unique")
    parser.add_argument("--save", type=int, default=3650)
    parser.add_argument("--kind", choices=["site", "database"], default="site")
    parser.add_argument("--site", default="ALL", help="ALL or one uniquely registered BaoTa website name")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check-only", action="store_true")
    mode.add_argument("--retry-archive", dest="retry_archives", action="append", help="repeat for existing native site archives in their fixed site or .failed-uploads directory")
    args = parser.parse_args()
    if args.save < 1:
        parser.error("Retention must be positive")
    if args.kind == "database" and args.site != "ALL":
        parser.error("Database mode backs up all native MySQL databases")
    if args.retry_archives and (args.kind != "site" or args.site != "ALL"):
        parser.error("Archive retry selects site targets from the validated archive paths")
    os.umask(0o077)
    try:
        run(args)
    except Exception as error:
        details = dict(safe_error_fields(error), phase=getattr(error, "phase", "runtime"),
            code=getattr(error, "code", "native_backup_failed"))
        if isinstance(error, NativeFailure):
            details.update(error.summary)
        print("NATIVE_BACKUP_FAILED " + details["error_type"] + " " + json.dumps(details), file=sys.stderr, flush=True)
        sys.exit(1)


if __name__ == "__main__":
    main()
