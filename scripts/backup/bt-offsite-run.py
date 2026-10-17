"""BaoTa cron: consistent project supplement, public-key encryption, verified Drive upload."""
import argparse
import base64
import binascii
import contextlib
import datetime
import fcntl
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import random
import re
import shutil
import socket
import ssl
import stat
import struct
import subprocess
import sys
import tempfile
import time
import uuid

OWNER = "bt-project-supplement-66.42.81.91-v1"
PREFIX = "bt-project-supplement-"
NATIVE_WEB_PARENT = "1jWsFZUzoXgiCQlDrWuTdPgiKzg1L7uA8"
NATIVE_DATABASE_PARENT = "1D6RQFgEyeIoVuplqZltXfefBb1fWyJiD"
GANWEN_FOLDER = "1tNW8vhlZx5HB3ecV4fx1WxnHn26o4JX0"
DRIVE_ATTEMPTS = 7
ENCRYPTED_MAGIC = b"BTBACKUP1\n"


class BackupFailure(RuntimeError):
    """Only fixed reason codes and locally generated context reach cron logs."""

    def __init__(self, code, message, phase=None, error_type=None,
                 diagnostics=None, uploaded=False):
        super().__init__(message)
        self.code, self.phase = code, phase
        self.error_type = error_type or type(self).__name__
        self.diagnostics, self.uploaded = diagnostics, uploaded


def drive_failure(error):
    """Classify HTTP responses without rendering URLs, headers or error bodies."""
    if isinstance(error, ssl.SSLError):
        certificate_error = getattr(ssl, "SSLCertVerificationError", ())
        if (isinstance(error, certificate_error)
                or "CERTIFICATE_VERIFY_FAILED" in str(error).upper()
                or getattr(error, "reason", "") == "CERTIFICATE_VERIFY_FAILED"):
            return "drive_tls_certificate_verification_failed", False
        return "drive_tls_transmission_interrupted", True
    response = getattr(error, "resp", None)
    status = getattr(response, "status", None)
    try:
        status = int(status) if status is not None else None
    except (TypeError, ValueError):
        status = None
    reasons, minute_quota = set(), False
    if status == 403:
        try:
            body = json.loads(getattr(error, "content", b"{}"))
            detail = body.get("error", {})
            errors = [item for item in detail.get("errors", []) if isinstance(item, dict)]
            reasons = {item.get("reason") for item in errors}
            messages = [detail.get("message")] + [item.get("message") for item in errors]
            minute_quota = ("quotaExceeded" in reasons and "storageQuotaExceeded" not in reasons
                            and any(isinstance(message, str) and re.search(
                                r"\b(?:requests|queries)\s+per\s+minute\b", message, re.I)
                                    for message in messages))
        except (TypeError, ValueError, AttributeError):
            pass
    if status == 403 and "storageQuotaExceeded" in reasons:
        return "drive_access_or_quota_denied", False
    if (status == 429 or minute_quota
            or reasons.intersection({"rateLimitExceeded", "userRateLimitExceeded"})):
        return "drive_rate_limited", True
    if status in (408, 500, 502, 503, 504) or "backendError" in reasons:
        return "drive_temporarily_unavailable", True
    if status == 401:
        return "drive_authentication_failed", False
    if status == 403:
        return "drive_access_or_quota_denied", False
    if isinstance(error, (TimeoutError, ConnectionError, socket.timeout)):
        return "drive_connection_interrupted", True
    return "drive_request_failed", False


def drive_request(request, operation, upload=False):
    """Retry the same resumable request; never start a second upload on failure."""
    for attempt in range(DRIVE_ATTEMPTS):
        try:
            method = request.next_chunk if upload else request.execute
            return method(num_retries=0)
        except Exception as error:
            code, retryable = drive_failure(error)
            if not retryable or attempt == DRIVE_ATTEMPTS - 1:
                raise
            delay = min(32, 2 ** attempt) + random.uniform(0, 1)
            print("DRIVE_RETRY " + json.dumps({"operation": operation, "code": code,
                "attempt": attempt + 1, "delay_seconds": round(delay, 2)}), flush=True)
            time.sleep(delay)


def failure_record(error, phase=None, diagnostics=None, uploaded=False):
    if isinstance(error, BackupFailure):
        return {"code": error.code, "phase": error.phase or phase or "check",
                "error_type": error.error_type, "diagnostics": error.diagnostics or diagnostics,
                "uploaded": error.uploaded or uploaded}
    code, _ = drive_failure(error)
    if code == "drive_request_failed":
        code = (phase or "backup") + "_failed"
    return {"code": code, "phase": phase or "check", "error_type": type(error).__name__,
            "diagnostics": diagnostics, "uploaded": uploaded}


def digest(path, algorithm="sha256"):
    value = hashlib.new(algorithm)
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def drive_service(panel):
    os.chdir(str(panel))
    sys.path.insert(0, str(panel / "class"))
    sys.path.insert(0, str(panel / "plugin/gdrive"))
    from gdrive_main import gdrive_main
    from googleapiclient.discovery import build
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
        client = gdrive_main()
        authenticated = client.set_creds()
    if not authenticated:
        raise BackupFailure("drive_authentication_unavailable", "Existing Google Drive authentication unavailable")
    credentials = getattr(client, "_gdrive_main__creds", None)
    if credentials is None:
        raise BackupFailure("drive_credentials_unavailable", "Existing Google Drive authentication unavailable")
    return build("drive", "v3", credentials=credentials, cache_discovery=False)


def verify_parent(service, folder):
    meta = drive_request(service.files().get(fileId=folder,
        fields="id,mimeType,trashed,shared,owners(emailAddress),capabilities(canAddChildren)"), "verify_folder")
    if (meta.get("mimeType") != "application/vnd.google-apps.folder" or meta.get("trashed")
            or meta.get("shared") is not False
            or [x.get("emailAddress") for x in meta.get("owners", [])] != ["ganwen33@gmail.com"]
            or not meta.get("capabilities", {}).get("canAddChildren")):
        raise BackupFailure("drive_folder_identity_failed", "Backup folder must be private, owned by the intended account and writable")
    return meta["id"]


def validate_encrypted(raw, directory, public_key_path):
    """Validate the existing public-key envelope without a recovery private key."""
    path = Path(raw)
    pattern = re.escape(PREFIX) + r"\d{8}T\d{6}Z-[0-9a-f]{12}\.tar\.gz\.enc"
    if (not path.is_absolute() or ".." in path.parts or path.parent != directory
            or re.fullmatch(pattern, path.name) is None or path.is_symlink()
            or not path.exists() or not stat.S_ISREG(path.lstat().st_mode)
            or path.resolve() != path):
        raise BackupFailure("retry_encrypted_path_invalid", "Retry file must be an existing encrypted backup in the backup directory")
    key_path = Path(public_key_path)
    try:
        from cryptography.hazmat.primitives import serialization
        from cryptography.hazmat.primitives.asymmetric import rsa
        if key_path.is_symlink() or not key_path.is_file():
            raise ValueError("Invalid public key file")
        public_key = serialization.load_pem_public_key(key_path.read_bytes())
        if not isinstance(public_key, rsa.RSAPublicKey) or public_key.key_size < 3072:
            raise ValueError("Invalid public key type")
    except Exception as error:
        raise BackupFailure("backup_public_key_invalid", "Existing RSA backup public key could not be verified") from error
    try:
        with path.open("rb") as stream:
            if stream.read(len(ENCRYPTED_MAGIC)) != ENCRYPTED_MAGIC:
                raise ValueError("Invalid magic")
            size_bytes = stream.read(4)
            if len(size_bytes) != 4:
                raise ValueError("Truncated header")
            size = struct.unpack(">I", size_bytes)[0]
            if not 0 < size < 65536:
                raise ValueError("Invalid header size")
            encoded = stream.read(size)
            header = json.loads(encoded.decode("utf-8"))
            if (not isinstance(header, dict) or type(header.get("version")) is not int
                    or header["version"] != 1
                    or header.get("cipher") != "AES-256-GCM+RSA-OAEP-SHA256"):
                raise ValueError("Invalid encrypted format")
            wrapped = base64.b64decode(header["key"], validate=True)
            nonce = base64.b64decode(header["nonce"], validate=True)
            if (len(wrapped) != (public_key.key_size + 7) // 8 or len(nonce) != 12
                    or not 0 < int.from_bytes(wrapped, "big") < public_key.public_numbers().n
                    or path.stat().st_size <= stream.tell() + 16):
                raise ValueError("Invalid encrypted payload")
    except (ValueError, TypeError, KeyError, UnicodeError, binascii.Error) as error:
        raise BackupFailure("retry_encrypted_format_invalid", "Existing encrypted backup header or payload is invalid") from error
    return path


def remote_receipt(service, path, parent, file_id, expected_size, expected_md5):
    meta = drive_request(service.files().get(fileId=file_id,
        fields="id,name,size,md5Checksum,parents,trashed,shared,appProperties"), "verify_upload")
    if (meta.get("id") != file_id or meta.get("name") != path.name
            or meta.get("trashed") or meta.get("shared") is not False
            or int(meta.get("size", "-1")) != expected_size
            or meta.get("md5Checksum", "").lower() != expected_md5
            or meta.get("parents") != [parent]
            or meta.get("appProperties", {}).get("backupOwner") != OWNER):
        raise BackupFailure("drive_upload_verification_failed", "Remote backup checksum, size, name, ownership marker or privacy check failed")
    return {"id": file_id, "name": path.name, "size": expected_size,
            "md5": expected_md5, "sha256": digest(path), "parent": parent}


def retry_upload_verified(service, path, parent):
    receipt_path = path.parent / (path.name + ".json")
    existing = None
    if receipt_path.exists() or receipt_path.is_symlink():
        if receipt_path.is_symlink() or not receipt_path.is_file():
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt is not a regular file")
        try:
            existing = json.loads(receipt_path.read_text(encoding="utf-8"))
        except (ValueError, UnicodeError) as error:
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt is invalid") from error
        expected = {"backup_owner": OWNER, "name": path.name, "parent": parent,
                    "size": path.stat().st_size, "md5": digest(path, "md5"), "sha256": digest(path)}
        if (not isinstance(existing, dict)
                or any(existing.get(key) != value for key, value in expected.items())
                or not isinstance(existing.get("id"), str)
                or re.fullmatch(r"[A-Za-z0-9_-]+", existing["id"]) is None
                or not isinstance(existing.get("completed_utc"), str) or not existing["completed_utc"]):
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt does not match the local encrypted backup")
        # A previous receipt is authoritative: an empty name listing must never
        # cause a new upload followed by an unreplaceable receipt-ID conflict.
        try:
            receipt = remote_receipt(service, path, parent, existing["id"], expected["size"], expected["md5"])
        except Exception as error:
            if getattr(getattr(error, "resp", None), "status", None) in (404, 410):
                raise BackupFailure("retry_receipt_cloud_missing", "The cloud backup referenced by the existing receipt is unavailable") from error
            raise
    # Include unowned same-name objects in this lookup so they cannot be
    # accidentally duplicated, overwritten, or accepted as our backup.
    query = "'{}' in parents and trashed=false and name='{}'".format(parent, path.name)
    matches = list_files(service, query, "id,name,parents")
    if len(matches) > 1:
        raise BackupFailure("retry_cloud_backup_ambiguous", "Multiple existing cloud backups have this name")
    if existing:
        if matches and matches[0].get("id") != existing["id"]:
            raise BackupFailure("retry_cloud_backup_ambiguous", "Same-name cloud backup conflicts with the existing receipt")
        print("BACKUP_UPLOAD_REUSED " + json.dumps({"id": receipt["id"], "size": receipt["size"]}), flush=True)
        return receipt
    if matches:
        file_id = matches[0].get("id")
        if not file_id:
            raise BackupFailure("retry_cloud_backup_id_missing", "Existing cloud backup has no usable ID")
        receipt = remote_receipt(service, path, parent, file_id, path.stat().st_size, digest(path, "md5"))
        print("BACKUP_UPLOAD_REUSED " + json.dumps({"id": file_id, "size": receipt["size"]}), flush=True)
        return receipt
    return upload_verified(service, path, parent)


def publish_receipt(directory, stage, receipt):
    receipt_path = directory / (receipt["name"] + ".json")
    if receipt_path.exists() or receipt_path.is_symlink():
        if receipt_path.is_symlink() or not receipt_path.is_file():
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt is not a regular file")
        try:
            record = json.loads(receipt_path.read_text(encoding="utf-8"))
        except (ValueError, UnicodeError) as error:
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt is invalid") from error
        fields = ("backup_owner", "name", "id", "parent", "size", "md5", "sha256")
        if (not isinstance(record, dict) or any(record.get(key) != receipt.get(key) for key in fields)
                or not isinstance(record.get("completed_utc"), str) or not record["completed_utc"]):
            raise BackupFailure("local_receipt_conflict", "Existing backup receipt does not match the verified upload")
        return record
    pending_receipt = stage / "receipt.json"
    with pending_receipt.open("x", encoding="utf-8") as file:
        os.chmod(str(pending_receipt), 0o600)
        json.dump(receipt, file, ensure_ascii=False, indent=2)
        file.flush()
        os.fsync(file.fileno())
    os.link(str(pending_receipt), str(receipt_path))
    return receipt


def upload_verified(service, path, parent):
    from googleapiclient.http import MediaFileUpload
    path = Path(path)
    expected_size, expected_md5 = path.stat().st_size, digest(path, "md5")
    body = {"name": path.name, "parents": [parent], "appProperties": {"backupOwner": OWNER}}
    request = service.files().create(body=body,
        media_body=MediaFileUpload(str(path), mimetype="application/octet-stream",
                                  chunksize=4 * 1024 * 1024, resumable=True), fields="id")
    result, reported = None, -1
    while result is None:
        progress, result = drive_request(request, "upload_chunk", upload=True)
        if progress:
            percent = int(progress.progress() * 100) // 25 * 25
            if percent > reported:
                print("Upload progress: {}%".format(percent), flush=True)
                reported = percent
    file_id = result.get("id")
    if not file_id:
        raise BackupFailure("drive_upload_id_missing", "Upload returned no file ID")
    return remote_receipt(service, path, parent, file_id, expected_size, expected_md5)


def retain_remote(service, parent, keep):
    query = ("'{}' in parents and trashed=false and appProperties has {{key='backupOwner' and value='{}'}}"
             .format(parent, OWNER))
    files, token = [], None
    while True:
        result = drive_request(service.files().list(q=query, fields="nextPageToken,files(id,name,createdTime,parents,appProperties)",
            pageSize=1000, pageToken=token), "list_supplemental_backups")
        files.extend(result.get("files", []))
        token = result.get("nextPageToken")
        if not token:
            break
    files.sort(key=lambda x: (x["createdTime"], x["id"]), reverse=True)
    for entry in files[keep:]:
        if (entry.get("parents") != [parent] or not entry.get("name", "").startswith(PREFIX)
                or not entry["name"].endswith(".tar.gz.enc")
                or entry.get("appProperties", {}).get("backupOwner") != OWNER):
            raise BackupFailure("remote_retention_identity_failed", "Refusing to trim an unowned backup")
        # Recoverable Drive Trash; never permanently delete remote backups.
        result = drive_request(service.files().update(fileId=entry["id"], body={"trashed": True}, fields="id,trashed"), "trash_supplemental_backup")
        if not result.get("trashed"):
            raise BackupFailure("remote_retention_verification_failed", "Remote retention could not be verified")
    return min(len(files), keep)


def retain_local(directory, keep, parent=None):
    successful, seen = [], set()
    for receipt in directory.glob(PREFIX + "*.tar.gz.enc.json"):
        if receipt.is_symlink() or not receipt.is_file():
            continue
        record = json.loads(receipt.read_text(encoding="utf-8"))
        file_path = directory / record.get("name", "")
        if (record.get("backup_owner") != OWNER or not record.get("id")
                or not record.get("parent") or (parent and record.get("parent") != parent)
                or receipt.name != file_path.name + ".json" or file_path in seen
                or file_path.parent != directory
                or not file_path.name.startswith(PREFIX) or not file_path.name.endswith(".tar.gz.enc")
                or file_path.is_symlink() or not file_path.is_file()
                or record.get("size") != file_path.stat().st_size
                or record.get("sha256") != digest(file_path)
                or record.get("md5") != digest(file_path, "md5")):
            raise BackupFailure("local_receipt_verification_failed", "Invalid local backup receipt; refusing cleanup")
        seen.add(file_path)
        successful.append((record["completed_utc"], file_path, receipt))
    successful.sort(reverse=True)
    for _, file_path, receipt in successful[keep:]:
        file_path.unlink()
        receipt.unlink()


def list_files(service, query, fields):
    result, token = [], None
    while True:
        page = drive_request(service.files().list(q=query, fields="nextPageToken,files(" + fields + ")",
                                   pageSize=1000, pageToken=token), "list_native_backups")
        result.extend(page.get("files", []))
        token = page.get("nextPageToken")
        if not token:
            return result


def retain_native(service, keep):
    """Enforce native 3650-file retention without the installed plugin's deletion bug."""
    import public
    sites = public.M("sites").field("name").select()
    databases = public.M("databases").field("name,type,sid").select()
    if not isinstance(sites, list) or not isinstance(databases, list):
        raise BackupFailure("native_metadata_query_failed", "Native backup metadata query failed")
    current = [(NATIVE_WEB_PARENT, {row["name"] for row in sites}, "web"),
               (NATIVE_DATABASE_PARENT,
                {row["name"] for row in databases if str(row.get("type", "")).lower() == "mysql"
                 and str(row.get("sid", "0")) == "0"}, "mysql")]
    counts, folder_cache = {}, {}
    for root, names, kind in current:
        verify_parent(service, root)
        folders = list_files(service, "'{}' in parents and trashed=false and mimeType='application/vnd.google-apps.folder'".format(root),
                             "id,name,parents")
        folder_cache[root] = folders
        for name in sorted(names):
            folder_name = "ganwen.nl" if kind == "web" and name == "ganwen_blog" else name
            matches = [item for item in folders if item.get("name") == folder_name and item.get("parents") == [root]]
            if not matches and kind == "mysql":
                # The installed plugin resolves names globally. A database
                # sharing a website name (currently language) uses that
                # website's original folder; preserve it and trim SQL only.
                matches = [item for item in folder_cache.get(NATIVE_WEB_PARENT, [])
                           if item.get("name") == name and item.get("parents") == [NATIVE_WEB_PARENT]]
            if len(matches) > 1:
                raise BackupFailure("native_folder_ambiguous", "Ambiguous native backup folder; refusing retention")
            if not matches:
                # A newly registered project is backed up by the next native ALL run.
                counts[kind + "/" + name] = "awaiting_native_run"
                continue
            folder = verify_parent(service, matches[0]["id"])
            if kind == "web":
                pattern = re.compile(r"^web_" + re.escape(name) + r"_\d{8}_\d{6}_[A-Za-z0-9]+\.tar\.gz$")
            else:
                pattern = re.compile(r"^" + re.escape(name) + r"_\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}_mysql_data\.sql\.gz$")
            files = list_files(service, "'{}' in parents and trashed=false".format(folder), "id,name,createdTime,parents")
            owned = [item for item in files if item.get("parents") == [folder] and pattern.fullmatch(item.get("name", ""))]
            owned.sort(key=lambda x: (x["createdTime"], x["id"]), reverse=True)
            for item in owned[keep:]:
                response = drive_request(service.files().update(fileId=item["id"], body={"trashed": True}, fields="id,trashed"), "trash_native_backup")
                if not response.get("trashed"):
                    raise BackupFailure("native_retention_verification_failed", "Native backup retention could not be verified")
            counts[kind + "/" + name] = min(len(owned), keep)
    return counts


def run(args):
    directory = Path(args.backup_dir).absolute()
    stage, phase, uploaded = None, "prepare_directory", False
    try:
        if directory.is_symlink():
            raise BackupFailure("backup_directory_symlink", "Backup directory must not be a symlink")
        directory.mkdir(parents=True, exist_ok=True)
        if directory.resolve() != directory:
            raise BackupFailure("backup_directory_symlink", "Backup directory must not traverse symlinks")
        os.chmod(str(directory), 0o700)
        here = Path(__file__).resolve().parent
        phase = "acquire_lock"
        with (directory / ".backup.lock").open("a+") as lock:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise BackupFailure("backup_already_running", "A project backup is already running")
            retry_encrypted = getattr(args, "retry_encrypted", None)
            if retry_encrypted:
                phase = "validate_encrypted"
                encrypted = validate_encrypted(retry_encrypted, directory, args.public_key)
            phase = "authenticate"
            service = drive_service(Path(args.panel))
            phase = "verify_folder"
            parent = verify_parent(service, args.folder)
            timestamp = datetime.datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")
            name = PREFIX + timestamp + "-" + uuid.uuid4().hex[:12] + ".tar.gz"
            phase = "prepare_stage"
            stage = Path(tempfile.mkdtemp(prefix=".project-run-", dir=str(directory)))
            os.chmod(str(stage), 0o700)
            if not retry_encrypted:
                encrypted = directory / (name + ".enc")
                plaintext = stage / name
                phase = "archive"
                subprocess.run([sys.executable, str(here / "bt-project-backup.py"),
                                "--output", str(plaintext)], check=True)
                phase = "encrypt"
                spec = importlib.util.spec_from_file_location("bt_backup_crypto", str(here / "bt-backup-crypto.py"))
                crypto = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(crypto)
                crypto.process(plaintext, encrypted, args.public_key)
                print("Encrypted archive ready: {} bytes".format(encrypted.stat().st_size), flush=True)
                # Keep private child diagnostics on failure, but avoid retaining
                # a second plaintext archive after encryption is complete.
                plaintext.unlink()
            else:
                print("BACKUP_RETRY_ENCRYPTED " + json.dumps({"name": encrypted.name,
                    "size": encrypted.stat().st_size}), flush=True)
            phase = "upload"
            receipt = (retry_upload_verified(service, encrypted, parent) if retry_encrypted
                       else upload_verified(service, encrypted, parent))
            uploaded = True
            print("BACKUP_UPLOAD_VERIFIED " + json.dumps({"id": receipt["id"],
                "parent": receipt["parent"], "size": receipt["size"]}), flush=True)
            receipt.update({"backup_owner": OWNER,
                            "completed_utc": datetime.datetime.utcnow().isoformat() + "Z",
                            "cloud_keep": args.cloud_keep, "local_keep": args.local_keep})
            phase = "publish_receipt"
            receipt = publish_receipt(directory, stage, receipt)
            phase = "retain_remote"
            count = retain_remote(service, parent, args.cloud_keep)
            phase = "retain_native"
            native_counts = retain_native(service, args.cloud_keep)
            phase = "retain_local"
            retain_local(directory, args.local_keep, parent)
            phase = "cleanup_stage"
            # Only this mkdtemp directory; source projects are never changed.
            if stage.parent == directory and stage.name.startswith(".project-run-") and not stage.is_symlink():
                shutil.rmtree(str(stage))
            print("BACKUP_SUCCESS " + json.dumps(dict(receipt, cloud_count=count, native_counts=native_counts)), flush=True)
    except Exception as error:
        retained = (stage is not None and stage.parent == directory
                    and stage.name.startswith(".project-run-")
                    and stage.is_dir() and not stage.is_symlink())
        details = failure_record(error, phase, str(stage) if retained else None, uploaded)
        if retained:
            try:
                with (stage / "failure.json").open("x", encoding="utf-8") as file:
                    os.chmod(str(stage / "failure.json"), 0o600)
                    json.dump(dict(details, backup_owner=OWNER, success=False), file)
            except OSError:
                pass  # Preserve the original error and existing child diagnostics.
        raise BackupFailure(details["code"], "Project backup failed", phase=details["phase"],
            error_type=details["error_type"], diagnostics=details["diagnostics"],
            uploaded=details["uploaded"]) from error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--panel", default="/www/server/panel")
    parser.add_argument("--backup-dir", default="/www/backup/project-supplement")
    parser.add_argument("--folder", default=GANWEN_FOLDER)
    parser.add_argument("--public-key", default="/www/server/panel/script/ganwen-project-backup/backup-public.pem")
    parser.add_argument("--cloud-keep", type=int, default=3650)
    parser.add_argument("--local-keep", type=int, default=7)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check-only", action="store_true")
    mode.add_argument("--retry-encrypted", help="retry an existing encrypted backup inside --backup-dir without rescanning projects")
    args = parser.parse_args()
    if args.cloud_keep < 1 or args.local_keep < 1:
        parser.error("Retention must be positive")
    os.umask(0o077)
    try:
        if args.check_only:
            service = drive_service(Path(args.panel))
            verify_parent(service, args.folder)
            print("CHECK_SUCCESS: existing Drive authentication and private backup folder verified")
        else:
            run(args)
    except Exception as error:
        # Library exception messages can contain credentials; never log them.
        details = failure_record(error)
        print("BACKUP_FAILED " + details["error_type"] + " " + json.dumps(details), file=sys.stderr, flush=True)
        sys.exit(1)


if __name__ == "__main__":
    main()
