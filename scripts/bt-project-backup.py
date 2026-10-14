#!/usr/bin/env python3
"""Consistent supplemental BaoTa project backup (Python 3.7+, no network calls).

The caller owns locking, encryption, upload and retention.  This command only
publishes a new private tar.gz after validating every archived byte.  It does
not replace BaoTa's ALL websites / ALL MySQL tasks: registered project files
are recorded as covered by those tasks, except for independent SQLite snapshots
and the complete, independently restorable Ganwen bundle.
"""

import argparse
from contextlib import closing, redirect_stdout, redirect_stderr
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import subprocess
import sys
import tarfile
import tempfile
import time


OWNER = 'ganwen-bt-project-backup-v1'
SQLITE_MAGIC = b'SQLite format 3\0'
SITE_FIELDS = ('id', 'name', 'path', 'project_type', 'status')
JOB_FIELDS = ('id', 'post_id', 'language', 'source_hash', 'status', 'attempts',
              'last_error', 'next_attempt_at', 'baseline_hash')
PANEL_METADATA_DATABASES = ('site.db', 'database.db', 'crontab.db')


class BackupError(RuntimeError):
    """Public error text contains only fixed reasons and filesystem paths."""

    def __init__(self, message, code=None, phase=None, error_type=None, path=None, diagnostics=None):
        super().__init__(message)
        self.code, self.phase = code, phase
        self.error_type = error_type or type(self).__name__
        self.backup_path, self.diagnostics = path, diagnostics


def source_path_type(path, context=None):
    """Classify known source roles without rendering a caller's filename."""
    context = context or {}
    path = Path(path)
    if str(path) in context.get('sqlite_paths', ()):
        return 'sqlite_database'
    if str(path) in context.get('sqlite_sidecars', ()):
        return 'sqlite_sidecar'
    if any(path == Path(root) for root in context.get('source_roots', ())):
        return 'source_root'
    parts = [part.lower() for part in path.parts]
    if (path.suffix.lower() in ('.env', '.conf', '.ini', '.yaml', '.yml', '.toml', '.key', '.pem', '.crt')
            or path.name.lower() in ('.env', 'config.php', 'wp-config.php', 'settings.json', 'runtime.env', 'admin.json')
            or any(part in ('config', 'configs', 'configuration') for part in parts)):
        return 'configuration'
    if path.suffix.lower() in ('.db', '.sqlite', '.sqlite3'):
        return 'database_file'
    if (any(part in ('uploads', 'media', 'evidence') for part in parts)
            or path.suffix.lower() in ('.jpg', '.jpeg', '.png', '.gif', '.webp', '.avif', '.svg', '.mp4', '.mov', '.webm', '.mp3', '.wav', '.ogg', '.pdf')):
        return 'original_media'
    if any(part == 'cache' for part in parts) or any(parts[index:index + 3] == ['storage', 'framework', 'views'] for index in range(len(parts) - 2)):
        return 'runtime_cache'
    return 'other_source'


def safe_path_diagnostics(path, context=None):
    context = context or {}
    path = Path(path)
    source = 'unknown'
    for label, roots in (
            ('private_staging', context.get('staging_roots', ())),
            ('ganwen_bundle', context.get('ganwen_roots', ())),
            ('panel_configuration', context.get('panel_roots', ())),
            ('native_registered_project', context.get('registered_roots', ())),
            ('supplemental_project', context.get('supplemental_roots', ())),
            ('wwwroot', context.get('wwwroot_roots', ()))):
        if any(within(path, Path(root)) for root in roots):
            source = label
            break
    return {'source': source, 'path_type': source_path_type(path, context),
            'path_sha256': hashlib.sha256(os.fsencode(str(path))).hexdigest()}


def inventory_missing(path, error, sqlite_observed=False):
    failure = BackupError('Required inventory path is missing', code='required_path_missing',
                          error_type=type(error).__name__, path=path)
    failure.sqlite_observed = sqlite_observed
    return failure


def failure_details(error, phase, context=None):
    """Expose stable reasons while excluding arbitrary library/CLI messages."""
    prefixes = (
        ('Required path is missing', 'required_path_missing'),
        ('Symbolic', 'unsafe_symbolic_link'),
        ('Unsupported special file', 'unsupported_special_file'),
        ('File changed', 'source_file_changed'),
        ('Log was', 'source_log_changed'),
        ('SQLite online snapshot timed out', 'sqlite_snapshot_timeout'),
        ('SQLite snapshot failed integrity check', 'sqlite_integrity_check_failed'),
        ('Ganwen backup command timed out', 'ganwen_cli_timeout'),
        ('Ganwen backup command failed', 'ganwen_cli_failed'),
        ('Ganwen logical backup has no matching separate key', 'ganwen_key_sidecar_failed'),
        ('Ganwen logical backup', 'ganwen_backup_format_failed'),
        ('Unsupported Ganwen logical backup format', 'ganwen_backup_format_failed'),
        ('Restored Ganwen', 'ganwen_restore_validation_failed'),
        ('Ganwen posts differ', 'ganwen_posts_restore_mismatch'),
        ('Ganwen settings differ', 'ganwen_settings_restore_mismatch'),
        ('Ganwen translation jobs differ', 'ganwen_jobs_restore_mismatch'),
        ('Ganwen analytics', 'ganwen_analytics_restore_failed'),
        ('Project roots changed', 'project_roots_changed'),
        ('SQLite database paths changed', 'sqlite_paths_changed'),
        ('Project file inventory changed', 'project_inventory_changed'),
        ('Vhost file inventory changed', 'vhost_inventory_changed'),
        ('Finished archive fingerprints differ', 'archive_fingerprints_failed'),
        ('Output was created concurrently', 'output_created_concurrently'),
        ('BaoTa sites metadata query failed', 'panel_metadata_query_failed'),
    )
    code = phase + '_failed'
    if isinstance(error, BackupError):
        code = error.code or next((value for prefix, value in prefixes
                                   if str(error).startswith(prefix)), code)
        phase = error.phase or phase
    elif isinstance(error, FileNotFoundError):
        code = 'required_path_missing'
    elif isinstance(error, PermissionError):
        code = 'filesystem_permission_denied'
    elif isinstance(error, sqlite3.DatabaseError):
        code = 'sqlite_database_error'
    details = {'code': code, 'phase': phase,
               'error_type': error.error_type if isinstance(error, BackupError) else type(error).__name__}
    path = getattr(error, 'backup_path', None) or getattr(error, 'filename', None)
    if isinstance(error, BackupError) and error.diagnostics is not None:
        details['path'] = error.diagnostics
    elif isinstance(path, (str, bytes, os.PathLike)):
        details['path'] = safe_path_diagnostics(os.fsdecode(path), context)
        if getattr(error, 'sqlite_observed', False) is True:
            details['path']['path_type'] = 'sqlite_database'
    return details


def within(path, boundary):
    try:
        path.relative_to(boundary)
        return True
    except ValueError:
        return False


def reject_link(path):
    return path.is_symlink() or bool(getattr(path.lstat(), 'st_file_attributes', 0) & 0x400)


def safe_path(raw, directory=None):
    path = Path(raw)
    if not path.is_absolute() or '..' in path.parts:
        raise BackupError('Expected an absolute path without traversal: ' + str(path))
    current = path
    while True:
        if current.exists() or current.is_symlink():
            if reject_link(current):
                raise BackupError('Symbolic/reparse project roots are not allowed: ' + str(current))
        if current == current.parent:
            break
        current = current.parent
    if not path.exists():
        raise BackupError('Required path is missing', path=path)
    if directory is True and not path.is_dir():
        raise BackupError('Expected a directory: ' + str(path))
    if directory is False and not stat.S_ISREG(path.lstat().st_mode):
        raise BackupError('Expected a regular file: ' + str(path))
    return path.resolve()


def unique_roots(paths):
    result = []
    for path in sorted(set(paths), key=lambda value: (len(value.parts), str(value))):
        if not any(within(path, parent) for parent in result):
            result.append(path)
    return sorted(result, key=str)


def build_plan(wwwroot, sites):
    wwwroot = safe_path(wwwroot, directory=True)
    if not isinstance(sites, list):
        raise BackupError('BaoTa sites query did not return a list')
    metadata = []
    registered = []
    for site in sites:
        if not isinstance(site, dict) or not isinstance(site.get('path'), str) or not site['path']:
            raise BackupError('A registered site has no usable project path')
        path = safe_path(site['path'])
        if path.is_file():
            # BaoTa Go projects register an executable, whereas PHP/proxy sites
            # and some Go projects register the project directory itself.
            path = safe_path(path.parent, directory=True)
        elif not path.is_dir():
            raise BackupError('Registered project is not a file/directory: ' + str(path))
        if path == path.parent:
            raise BackupError('A filesystem root cannot be a project root: ' + str(path))
        registered.append(path)
        row = {field: site.get(field) for field in SITE_FIELDS}
        row['root'] = str(path)
        metadata.append(row)
    registered = unique_roots(registered)
    direct = []
    loose = []
    for path in sorted(wwwroot.iterdir(), key=lambda value: value.name):
        if reject_link(path):
            raise BackupError('Symbolic/reparse project roots are not allowed: ' + str(path))
        if path.is_dir():
            direct.append(path.resolve())
        elif path.is_file():
            loose.append(str(path))
        elif not stat.S_ISSOCK(path.lstat().st_mode):
            raise BackupError('Unsupported wwwroot entry: ' + str(path))
    uncovered = [path for path in direct if not any(within(path, root) for root in registered)]
    # A native site nested inside a previously unregistered directory only
    # covers that subtree.  Preserve its siblings without repeating the site.
    exclusions = {str(path): [str(root) for root in registered
                              if root != path and within(root, path)]
                  for path in uncovered}
    return {'wwwroot': str(wwwroot), 'registered_roots': registered,
            'uncovered_roots': uncovered,
            'project_roots': unique_roots(direct + registered),
            'excluded_registered_subroots': exclusions,
            'sites': metadata, 'wwwroot_loose_files': loose}


def file_signature(value):
    return (value.st_dev, value.st_ino, value.st_size,
            getattr(value, 'st_mtime_ns', int(value.st_mtime * 1000000000)), value.st_mode)


def appended_log(before, after):
    return (before.st_dev, before.st_ino, before.st_mode) == (after.st_dev, after.st_ino, after.st_mode) and (
        after.st_size > before.st_size or file_signature(before) == file_signature(after))


def collect_inventory(roots, transient_paths=None):
    """Inspect every file; preserve internal links and record transient sockets.

    Directory symlinks are never traversed.  Their resolved target must already
    be inside an explicitly inventoried root, so they cannot hide external data.
    No cache/vendor/log directory is excluded.
    """
    roots = unique_roots([safe_path(root, directory=True) for root in roots])
    if (os.name == 'posix' and hasattr(os, 'O_DIRECTORY') and hasattr(os, 'O_NOFOLLOW')
            and os.open in os.supports_dir_fd and os.scandir in os.supports_fd):
        return collect_inventory_posix(roots, transient_paths)
    return collect_inventory_legacy(roots, transient_paths)


def collect_inventory_posix(roots, transient_paths=None):
    """Walk anchored directory descriptors; inspect each real file exactly once.

    O_NOFOLLOW is enforced on every root path component and child directory.
    Files are opened relative to the held parent descriptor, avoiding repeated
    parent lstat/resolve work and preventing directory-replacement link races.
    Only traversal-depth descriptors remain open, never one per queued folder.
    """
    directory_flags = (os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
                       | getattr(os, 'O_CLOEXEC', 0))
    file_flags = (os.O_RDONLY | os.O_NOFOLLOW | getattr(os, 'O_CLOEXEC', 0)
                  | getattr(os, 'O_NONBLOCK', 0))
    records = []
    inspected = 0
    transient_paths = set(str(path) for path in (transient_paths or ()))

    def add_row(path, root, info):
        return {'path': str(path), 'root': str(root),
                'relative': path.relative_to(root).as_posix(),
                'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}

    def same_identity(first, second):
        return (first.st_dev, first.st_ino) == (second.st_dev, second.st_ino)

    def open_root(root):
        if not root.is_absolute() or '..' in root.parts:
            raise BackupError('Expected an absolute project root without traversal: ' + str(root))
        descriptor = os.open(root.anchor, directory_flags)
        try:
            for component in root.parts[1:]:
                child = os.open(component, directory_flags, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = child
            return descriptor
        except Exception as error:
            os.close(descriptor)
            if isinstance(error, FileNotFoundError):
                raise inventory_missing(root, error) from error
            raise

    def frame(path, descriptor):
        try:
            return (path, descriptor, os.scandir(descriptor))
        except Exception:
            os.close(descriptor)
            raise

    for root in roots:
        root = Path(root)
        descriptor = open_root(root)
        root_row = add_row(root, root, os.fstat(descriptor))
        root_row['kind'] = 'directory'
        records.append(root_row)
        frames = [frame(root, descriptor)]
        try:
            while frames:
                parent, parent_fd, entries = frames[-1]
                try:
                    entry = next(entries)
                except StopIteration:
                    frames.pop()
                    entries.close()
                    os.close(parent_fd)
                    continue
                path = parent / entry.name
                transient = str(path) in transient_paths
                try:
                    info = entry.stat(follow_symlinks=False)
                except FileNotFoundError as error:
                    if transient:
                        continue
                    raise inventory_missing(path, error) from error
                row = add_row(path, root, info)
                if stat.S_ISLNK(info.st_mode):
                    target_text = os.readlink(entry.name, dir_fd=parent_fd)
                    current = os.stat(entry.name, dir_fd=parent_fd, follow_symlinks=False)
                    if not same_identity(info, current) or not stat.S_ISLNK(current.st_mode):
                        raise BackupError('Symbolic link changed during inventory: ' + str(path))
                    target_path = Path(target_text)
                    if not target_path.is_absolute():
                        target_path = parent / target_path
                    target = target_path.resolve(strict=True)
                    if not any(within(target, boundary) for boundary in roots):
                        raise BackupError('Symbolic link escapes project roots: ' + str(path))
                    row.update(kind='symlink', target=target_text)
                elif stat.S_ISDIR(info.st_mode):
                    try:
                        child_fd = os.open(entry.name, directory_flags, dir_fd=parent_fd)
                    except FileNotFoundError as error:
                        raise inventory_missing(path, error) from error
                    try:
                        if not same_identity(info, os.fstat(child_fd)):
                            raise BackupError('Directory changed during inventory: ' + str(path))
                    except Exception:
                        os.close(child_fd)
                        raise
                    row['kind'] = 'directory'
                    records.append(row)
                    frames.append(frame(path, child_fd))
                    inspected += 1
                    if inspected % 10000 == 0:
                        print('SCANNED_ENTRIES {}'.format(inspected), file=sys.stderr, flush=True)
                    continue
                elif stat.S_ISREG(info.st_mode):
                    try:
                        file_fd = os.open(entry.name, file_flags, dir_fd=parent_fd)
                    except FileNotFoundError as error:
                        if transient:
                            continue
                        raise inventory_missing(path, error) from error
                    try:
                        before = os.fstat(file_fd)
                        if not stat.S_ISREG(before.st_mode) or (not transient and not same_identity(info, before)):
                            raise BackupError('File changed during inventory: ' + str(path))
                        is_sqlite = os.read(file_fd, 16) == SQLITE_MAGIC
                        after = os.fstat(file_fd)
                        changed = (file_signature(info) != file_signature(before)
                                   or file_signature(before) != file_signature(after))
                        log_append = path.suffix.lower() == '.log' and appended_log(info, before) and appended_log(before, after)
                        if not is_sqlite and not transient and changed and not log_append:
                            raise BackupError('File changed during inventory: ' + str(path))
                    finally:
                        os.close(file_fd)
                    row.update(kind='file', size=before.st_size,
                               signature=file_signature(before), sqlite=is_sqlite)
                elif stat.S_ISSOCK(info.st_mode):
                    row.update(kind='socket', restore='recreated by the owning service')
                else:
                    raise BackupError('Unsupported special file: ' + str(path))
                records.append(row)
                inspected += 1
                if inspected % 10000 == 0:
                    print('SCANNED_ENTRIES {}'.format(inspected), file=sys.stderr, flush=True)
        finally:
            for parent, parent_fd, entries in reversed(frames):
                entries.close()
                os.close(parent_fd)
    return records


def collect_inventory_legacy(roots, transient_paths=None):
    """Portable fallback for Windows without descriptor-relative filesystem APIs."""
    records = []
    transient_paths = set(str(path) for path in (transient_paths or ()))
    for root in roots:
        pending = [root]
        while pending:
            path = pending.pop()
            transient = str(path) in transient_paths
            try:
                info = path.lstat()
            except FileNotFoundError as error:
                if transient:
                    continue
                raise inventory_missing(path, error) from error
            row = {'path': str(path), 'root': str(root),
                   'relative': path.relative_to(root).as_posix(),
                   'mode': stat.S_IMODE(info.st_mode),
                   'uid': info.st_uid, 'gid': info.st_gid}
            if reject_link(path):
                # Mocked or Windows reparse points without an actual POSIX
                # link are rejected too: their target cannot be proved safe.
                if not stat.S_ISLNK(info.st_mode):
                    raise BackupError('Unsupported reparse point: ' + str(path))
                target = path.resolve(strict=True)
                if not any(within(target, boundary) for boundary in roots):
                    raise BackupError('Symbolic link escapes project roots: ' + str(path))
                row.update(kind='symlink', target=os.readlink(str(path)))
            elif stat.S_ISDIR(info.st_mode):
                row['kind'] = 'directory'
                pending.extend(reversed(sorted(path.iterdir(), key=lambda value: value.name)))
            elif stat.S_ISREG(info.st_mode):
                row.update(kind='file', size=info.st_size, signature=file_signature(info))
                try:
                    with open_regular(path) as stream:
                        row['sqlite'] = stream.read(16) == SQLITE_MAGIC
                    after = path.lstat()
                except (FileNotFoundError, BackupError) as error:
                    if transient and row.get('sqlite') is not True and not path.exists() and not path.is_symlink():
                        continue
                    if isinstance(error, FileNotFoundError):
                        raise inventory_missing(path, error, sqlite_observed=row.get('sqlite') is True) from error
                    raise
                log_append = path.suffix.lower() == '.log' and appended_log(info, after)
                if not row['sqlite'] and not transient and file_signature(after) != row['signature'] and not log_append:
                    raise BackupError('File changed during inventory: ' + str(path))
            elif stat.S_ISSOCK(info.st_mode):
                row.update(kind='socket', restore='recreated by the owning service')
            else:
                raise BackupError('Unsupported special file: ' + str(path))
            records.append(row)
    return records


def open_regular(path):
    safe_path(path, directory=False)
    flags = os.O_RDONLY | getattr(os, 'O_BINARY', 0) | getattr(os, 'O_NOFOLLOW', 0)
    descriptor = os.open(str(path), flags)
    if not stat.S_ISREG(os.fstat(descriptor).st_mode):
        os.close(descriptor)
        raise BackupError('Expected a regular file: ' + str(path))
    return os.fdopen(descriptor, 'rb')


def discover_sqlite(project_roots):
    return [Path(row['path']) for row in collect_inventory(project_roots)
            if row.get('sqlite')]


def digest(path):
    value = hashlib.sha256()
    with open_regular(path) as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def private_mkdir(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if reject_link(path) or not path.is_dir():
        raise BackupError('Private staging is not a real directory: ' + str(path))
    os.chmod(str(path), 0o700)


def backup_sqlite(source, destination, timeout=120):
    """SQLite online backup includes committed WAL content, never raw WAL copies."""
    source = safe_path(source, directory=False)
    destination = Path(destination)
    if destination.exists() or destination.is_symlink():
        raise BackupError('Snapshot destination already exists: ' + str(destination))
    private_mkdir(destination.parent)
    deadline = time.monotonic() + timeout

    def progress(status, remaining, total):
        if time.monotonic() > deadline:
            raise BackupError('SQLite online snapshot timed out: ' + str(source))

    try:
        with closing(sqlite3.connect(source.as_uri() + '?mode=ro', uri=True, timeout=5)) as live:
            with closing(sqlite3.connect(str(destination))) as snapshot:
                live.backup(snapshot, pages=256, progress=progress, sleep=0.1)
                if snapshot.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
                    raise BackupError('SQLite snapshot failed integrity check: ' + str(source))
                snapshot.commit()
        os.chmod(str(destination), 0o600)
    except Exception:
        for suffix in ('', '-wal', '-shm', '-journal'):
            path = Path(str(destination) + suffix)
            if path.exists() and not path.is_symlink():
                path.unlink()
        raise
    return {'source': str(source), 'sha256': digest(destination),
            'bytes': destination.stat().st_size, 'integrity_check': 'ok'}


def copy_regular(source, destination):
    source = Path(source)
    before = source.lstat()
    is_log = source.suffix.lower() == '.log'
    private_mkdir(destination.parent)
    with open_regular(source) as stream, destination.open('xb') as target:
        opened = os.fstat(stream.fileno())
        if is_log:
            if not appended_log(before, opened):
                raise BackupError('Log was replaced or truncated before copying: ' + str(source))
            remaining = opened.st_size
            while remaining:
                block = stream.read(min(remaining, 1024 * 1024))
                if not block:
                    raise BackupError('Log was truncated while copying: ' + str(source))
                target.write(block)
                remaining -= len(block)
        else:
            shutil.copyfileobj(stream, target, 1024 * 1024)
        after_fd = os.fstat(stream.fileno())
        after_path = source.lstat()
        if is_log:
            stable = appended_log(opened, after_fd) and appended_log(opened, after_path)
        else:
            stable = (file_signature(before) == file_signature(opened) ==
                      file_signature(after_fd) == file_signature(after_path))
        if not stable:
            raise BackupError('File changed while copying: ' + str(source))
    os.chmod(str(destination), 0o600)
    result = {'bytes': destination.stat().st_size, 'sha256': digest(destination)}
    if is_log:
        result.update(snapshot_mode='snapshot-prefix', snapshot_prefix_bytes=opened.st_size)
    return result


def write_json(path, document):
    private_mkdir(path.parent)
    with path.open('x', encoding='utf-8') as stream:
        json.dump(document, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    os.chmod(str(path), 0o600)


def load_sites(panel_root):
    panel_root = safe_path(panel_root, directory=True)
    sys.path.insert(0, str(panel_root / 'class'))
    sys.path.insert(0, str(panel_root))
    previous = os.getcwd()
    try:
        os.chdir(str(panel_root))
        # BaoTa libraries can print diagnostics themselves.  Do not expose
        # those strings, SQL/config values or import-time chatter to cron logs.
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            import public
            sites = public.M('sites').field(','.join(SITE_FIELDS)).select()
    finally:
        os.chdir(previous)
    if not isinstance(sites, list):
        raise BackupError('BaoTa sites metadata query failed')
    return sites


def safe_cli(argv, logpath):
    # Only the two early-return database subcommands are allowed.  Their code
    # never constructs NewApp or starts a translation worker.
    if len(argv) < 2 or argv[1] not in ('database-export', 'database-import'):
        raise BackupError('Unsupported Ganwen backup subcommand')
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith('GBLOG_')}
    with logpath.open('xb') as log:
        os.chmod(str(logpath), 0o600)
        try:
            result = subprocess.run(argv, env=environment, stdout=log, stderr=log,
                                    timeout=300, check=False)
        except subprocess.TimeoutExpired:
            raise BackupError('Ganwen backup command timed out')
    if result.returncode != 0:
        raise BackupError('Ganwen backup command failed; private diagnostic: ' + str(logpath))


def verify_ganwen_restore(data, document):
    if document.get('format') != 'ganwen-blog-database' or document.get('version') != 1:
        raise BackupError('Unsupported Ganwen logical backup format')
    for field in ('posts', 'values', 'translation_jobs'):
        if not isinstance(document.get(field), list):
            raise BackupError('Ganwen logical backup is incomplete')
    database = safe_path(data / 'blog.db', directory=False)
    with closing(sqlite3.connect(database.as_uri() + '?mode=ro', uri=True)) as restored:
        if restored.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
            raise BackupError('Restored Ganwen database failed integrity check')
        posts = [json.loads(row[0]) for row in restored.execute('SELECT document FROM posts ORDER BY id')]
        # Go regenerates its internal cache token during every logical restore.
        values = [{'key': row[0], 'value': row[1]}
                  for row in restored.execute('SELECT key,value FROM kv ORDER BY key')
                  if row[0] != 'posts_revision_v1']
        jobs = [dict(zip(JOB_FIELDS, row)) for row in restored.execute(
            'SELECT ' + ','.join(JOB_FIELDS) + ' FROM translation_jobs ORDER BY id')]
        if posts != sorted(document['posts'], key=lambda post: post['id']):
            raise BackupError('Ganwen posts differ after isolated restore')
        expected_values = [value for value in document['values']
                           if value['key'] != 'posts_revision_v1']
        if values != sorted(expected_values, key=lambda value: value['key']):
            raise BackupError('Ganwen settings differ after isolated restore')
        if jobs != sorted(document['translation_jobs'], key=lambda job: job['id']):
            raise BackupError('Ganwen translation jobs differ after isolated restore')
        analytics = document.get('analytics')
        analytics_rows = 0
        if analytics is not None:
            if not isinstance(analytics, dict):
                raise BackupError('Ganwen analytics backup is incomplete')
            for field, table, columns in (
                    ('aggregates', 'analytics_aggregates', ('date', 'kind', 'label', 'post_id', 'pv', 'uv')),
                    ('visitors', 'analytics_visitors', ('date', 'hash')),
                    ('events', 'analytics_events', ('date', 'hash'))):
                expected = analytics.get(field)
                if not isinstance(expected, list):
                    raise BackupError('Ganwen analytics backup is incomplete')
                sql_columns = ['id_hash' if name == 'hash' else name for name in columns]
                actual = [dict(zip(columns, row)) for row in restored.execute(
                    'SELECT ' + ','.join(sql_columns) + ' FROM ' + table)]
                order = lambda row: tuple(row[name] for name in columns)
                if sorted(actual, key=order) != sorted(expected, key=order):
                    raise BackupError('Ganwen analytics differ after isolated restore')
                analytics_rows += len(actual)
    expected_key = document.get('encryption_key_sha256')
    if expected_key:
        key = safe_path(data / 'translation.key', directory=False)
        if digest(key) != expected_key:
            raise BackupError('Restored Ganwen translation key fingerprint differs')
    return {'posts': len(posts), 'kv': len(values), 'translation_jobs': len(jobs),
            'analytics_rows': analytics_rows,
            'integrity_check': 'ok', 'worker_started': False}


def export_ganwen(ganwen_root, stage, runner=None):
    root = safe_path(ganwen_root, directory=True)
    binary = safe_path(root / 'ganwen', directory=False)
    data = safe_path(root / 'data', directory=True)
    runner = runner or safe_cli
    folder = stage / 'ganwen'
    private_mkdir(folder)
    document_path = folder / 'database.json'
    runner([str(binary), 'database-export', '--data', str(data), '--file', str(document_path)],
           stage / 'export-private.log')
    safe_path(document_path, directory=False)
    with document_path.open('r', encoding='utf-8') as stream:
        document = json.load(stream)
    key_hash = document.get('encryption_key_sha256')
    key = Path(str(document_path) + '.translation.key')
    if key_hash and (not key.is_file() or digest(key) != key_hash):
        raise BackupError('Ganwen logical backup has no matching separate key')
    restore = stage / 'isolated-restore'
    private_mkdir(restore)
    runner([str(binary), 'database-import', '--database', 'sqlite', '--data', str(restore),
            '--file', str(document_path)], stage / 'import-private.log')
    result = verify_ganwen_restore(restore, document)
    # Do not include the validation database: the original SQL snapshots and
    # database.json + its sidecar are the explicit restore sources.
    shutil.rmtree(str(restore))
    result.update(logical_backup='ganwen/database.json', source=str(data),
                  key_sidecar='ganwen/database.json.translation.key' if key_hash else None)
    return result


def archive_path(prefix, source):
    # Encode absolute restore paths as relative archive member names.
    relative = source.as_posix().lstrip('/')
    if source.drive:  # Isolated Windows fixtures, never used by BT production.
        relative = source.drive.rstrip(':').replace('\\', '/') + '/' + '/'.join(source.parts[1:])
    return Path(prefix) / Path(relative)


def inventory_identity(records):
    return sorted((row['path'], row['kind'], row.get('target'), bool(row.get('sqlite')))
                  for row in records)


def sqlite_sidecar_paths(databases):
    databases = set(str(path) for path in databases)
    return {path + suffix for path in databases for suffix in ('-wal', '-shm', '-journal')} - databases


def archive_inventory(records, plan, ganwen_root, sidecars):
    """Only files owned by this supplemental archive gate final membership."""
    wwwroot = Path(plan['wwwroot'])
    uncovered = [(root, [Path(value) for value in plan['excluded_registered_subroots'][str(root)]])
                 for root in plan['uncovered_roots']]
    selected = []
    for row in records:
        path = Path(row['path'])
        if row['path'] in sidecars:
            continue
        if within(path, ganwen_root) or path == wwwroot or (path.parent == wwwroot and row['kind'] != 'directory'):
            selected.append(row)
        elif any(within(path, root) and not any(within(path, excluded) for excluded in exclusions)
                 for root, exclusions in uncovered):
            selected.append(row)
    return selected


def native_optional_cache_paths(records, plan, ganwen_root):
    """Only previously inspected native-owned runtime files may disappear.

    Keep scanning these directories for new SQLite files. No Ganwen bundle,
    source root, directory, configuration or original media becomes optional.
    """
    prefixes = (('cache',), ('var', 'cache'), ('runtime', 'cache'),
                ('wp-content', 'cache'), ('storage', 'framework', 'cache'),
                ('storage', 'framework', 'views'))
    optional = set()
    for row in records:
        path = Path(row['path'])
        if row['kind'] != 'file' or row.get('sqlite') or within(path, ganwen_root):
            continue
        if source_path_type(path) != 'runtime_cache':
            continue
        for root in plan['registered_roots']:
            if not within(path, root):
                continue
            parts = tuple(part.lower() for part in path.relative_to(root).parts)
            if any(len(parts) > len(prefix) and parts[:len(prefix)] == prefix for prefix in prefixes):
                optional.add(str(path))
    return optional


def stage_tree(records, stage, prefix, exclusions, snapshots, manifest):
    sidecars = sqlite_sidecar_paths(snapshots)
    for row in records:
        source = Path(row['path'])
        if any(within(source, excluded) for excluded in exclusions):
            continue
        if str(source) in sidecars:
            continue
        destination = stage / archive_path(prefix, source)
        name = destination.relative_to(stage).as_posix()
        mapping = {'archive': name, 'restore_path': str(source),
                   'kind': row['kind'], 'mode': row['mode'],
                   'uid': row['uid'], 'gid': row['gid']}
        if row['kind'] == 'directory':
            private_mkdir(destination)
        elif row['kind'] == 'socket':
            mapping['restore'] = row['restore']
            manifest['skipped_sockets'].append(mapping)
            continue
        elif row['kind'] == 'symlink':
            private_mkdir(destination.parent)
            destination.symlink_to(row['target'])
            mapping['target'] = row['target']
        else:
            # WAL, SHM and rollback journals accompany a snapshotted database;
            # restoring them over the consistent backup would corrupt it.
            if source in snapshots:
                mapping.update(copy_regular(snapshots[source], destination))
                mapping['sqlite_online_snapshot'] = True
            else:
                mapping.update(copy_regular(source, destination))
        manifest['restore_entries'].append(mapping)


def pack_verified(stage, output):
    private_logs = {stage / 'export-private.log', stage / 'import-private.log'}
    paths = sorted((path for path in stage.rglob('*') if path not in private_logs), key=str)
    files = {path.relative_to(stage).as_posix(): digest(path)
             for path in paths if path.is_file() and not path.is_symlink()}
    links = {path.relative_to(stage).as_posix(): os.readlink(str(path))
             for path in paths if path.is_symlink()}
    with tarfile.open(str(output), 'x:gz', compresslevel=6) as archive:
        for path in paths:
            name = path.relative_to(stage).as_posix()
            info = archive.gettarinfo(str(path), arcname=name)
            info.uid = info.gid = 0
            info.uname = info.gname = 'root'
            # Original permissions are recorded in manifest.  Every archive
            # member is private until the operator restores the mapped paths.
            info.mode = 0o700 if info.isdir() else 0o600
            if info.isfile():
                with open_regular(path) as stream:
                    archive.addfile(info, stream)
            elif info.isdir() or info.issym():
                archive.addfile(info)
            else:
                raise BackupError('Unsupported staged archive member: ' + str(path))
    os.chmod(str(output), 0o600)
    actual_files = {}
    actual_links = {}
    seen = set()
    with tarfile.open(str(output), 'r:gz') as archive:
        for member in archive:
            if member.name in seen or member.name.startswith('/') or '..' in Path(member.name).parts:
                raise BackupError('Unsafe or duplicate archive member')
            seen.add(member.name)
            if member.isfile():
                value = hashlib.sha256()
                with archive.extractfile(member) as stream:
                    for block in iter(lambda: stream.read(1024 * 1024), b''):
                        value.update(block)
                actual_files[member.name] = value.hexdigest()
            elif member.issym():
                actual_links[member.name] = member.linkname
            elif not member.isdir():
                raise BackupError('Unsupported finished archive member')
    if files != actual_files or links != actual_links:
        raise BackupError('Finished archive fingerprints differ from staging')
    return len(files)


def run_backup(output, wwwroot, panel_root, sites, ganwen_root, runner=None):
    output = Path(output)
    if not output.is_absolute() or '..' in output.parts or output.exists() or output.is_symlink():
        raise BackupError('Output must be a new absolute tar.gz path: ' + str(output))
    if not output.name.endswith('.tar.gz'):
        raise BackupError('Output must end with .tar.gz: ' + str(output))
    parent = safe_path(output.parent, directory=True)
    plan = build_plan(wwwroot, sites)
    ganwen_root = safe_path(ganwen_root, directory=True)
    vhost = safe_path(Path(panel_root) / 'vhost', directory=True)
    panel_databases = [safe_path(Path(panel_root) / 'data/db' / name, directory=False)
                       for name in PANEL_METADATA_DATABASES]
    roots = unique_roots(plan['project_roots'] + [ganwen_root, safe_path(wwwroot, directory=True)])
    if any(within(parent, root) for root in roots + [vhost] + [path.parent for path in panel_databases]):
        raise BackupError('Backup output must be outside all source project/config trees')
    stage = Path(tempfile.mkdtemp(prefix='.bt-project-stage-', dir=str(parent)))
    os.chmod(str(stage), 0o700)
    partial = stage / 'archive.partial.tar.gz'
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    diagnostic_context = {'source_roots': roots + plan['registered_roots'] + plan['uncovered_roots'] + [vhost], 'ganwen_roots': [ganwen_root],
                          'registered_roots': plan['registered_roots'],
                          'supplemental_roots': plan['uncovered_roots'],
                          'panel_roots': [Path(panel_root)], 'wwwroot_roots': [Path(wwwroot)],
                          'staging_roots': [stage]}
    phase = 'inventory'
    try:
        inventory = collect_inventory(roots)
        diagnostic_context['sqlite_paths'] = {row['path'] for row in inventory if row.get('sqlite')}
        diagnostic_context['sqlite_sidecars'] = sqlite_sidecar_paths(diagnostic_context['sqlite_paths'])
        vhost_inventory = collect_inventory([vhost])
        manifest = {'format': OWNER, 'version': 1, 'created_at': started,
                    'native_all_required': {'websites': True, 'mysql_databases': True},
                    'registered_roots': [str(path) for path in plan['registered_roots']],
                    'supplemental_roots': [str(path) for path in plan['uncovered_roots']],
                    'excluded_registered_subroots': plan['excluded_registered_subroots'],
                    'wwwroot_loose_files': plan['wwwroot_loose_files'],
                    'restore_entries': [], 'sqlite_snapshots': [], 'skipped_sockets': [],
                    'panel_metadata_snapshots': [],
                    'ganwen_independent_bundle': str(ganwen_root),
                    'restore_notes': ['Restore archive members using restore_path; preserve original mode.',
                                     'Do not restore live WAL/SHM/journal files over SQLite snapshots.',
                                     'Ganwen logical database-import returns before starting any worker.',
                                     'Panel metadata snapshots are for isolated inspection/reconstruction; do not overwrite a running panel.',
                                     'Registered project files other than Ganwen require native ALL website backups.',
                                     'MySQL databases require the existing native ALL MySQL backup.']}
        snapshots = {}
        phase = 'sqlite_snapshots'
        for row in inventory:
            if row.get('sqlite'):
                source = Path(row['path'])
                target = stage / archive_path('sqlite', source)
                record = backup_sqlite(source, target)
                record['archive'] = target.relative_to(stage).as_posix()
                record['restore_path'] = str(source)
                record.update(mode=row['mode'], uid=row['uid'], gid=row['gid'])
                manifest['sqlite_snapshots'].append(record)
                snapshots[source] = target
        phase = 'panel_metadata_snapshots'
        for source in panel_databases:
            target = stage / archive_path('panel-metadata', source)
            record = backup_sqlite(source, target)
            source_info = source.lstat()
            record.update(archive=target.relative_to(stage).as_posix(), restore_path=str(source),
                          mode=stat.S_IMODE(source_info.st_mode), uid=source_info.st_uid,
                          gid=source_info.st_gid,
                          restore='isolated inspection/reconstruction; never overwrite a running panel')
            manifest['panel_metadata_snapshots'].append(record)
        phase = 'ganwen_export_restore'
        manifest['ganwen_validation'] = export_ganwen(ganwen_root, stage, runner)
        phase = 'stage_files'
        write_json(stage / 'metadata/sites.json', plan['sites'])
        # Archive only the uncovered pieces of project directories.  Ganwen is
        # deliberately duplicated as a complete standalone recovery bundle.
        for root in plan['uncovered_roots']:
            rows = [row for row in inventory if within(Path(row['path']), root)]
            exclusions = [Path(value) for value in plan['excluded_registered_subroots'][str(root)]]
            stage_tree(rows, stage, 'projects', exclusions, snapshots, manifest)
        wwwroot_path = Path(plan['wwwroot'])
        loose_rows = [row for row in inventory if Path(row['path']) == wwwroot_path or
                      (Path(row['path']).parent == wwwroot_path and row['kind'] != 'directory')]
        stage_tree(loose_rows, stage, 'projects', [], snapshots, manifest)
        ganwen_rows = [row for row in inventory if within(Path(row['path']), ganwen_root)]
        stage_tree(ganwen_rows, stage, 'ganwen-bundle', [], snapshots, manifest)
        stage_tree(vhost_inventory, stage, 'panel-vhost', [], {}, manifest)
        # Dynamic directories are not silently omitted if a project was created
        # or a new database appeared while the backup was being assembled.
        phase = 'final_inventory'
        final_plan = build_plan(wwwroot, sites)
        if final_plan['project_roots'] != plan['project_roots']:
            raise BackupError('Project roots changed during backup; rerun required')
        sidecars = sqlite_sidecar_paths(snapshots)
        optional_cache = native_optional_cache_paths(inventory, plan, ganwen_root)
        final_inventory = collect_inventory(roots, transient_paths=sidecars | optional_cache)
        final_paths = {row['path'] for row in final_inventory}
        for row in inventory:
            if row['path'] not in final_paths and source_path_type(row['path'], diagnostic_context) in ('source_root', 'configuration', 'original_media', 'database_file'):
                raise BackupError('Required source path is missing', code='required_path_missing', path=row['path'])
        final_databases = {Path(row['path']) for row in final_inventory if row.get('sqlite')}
        if final_databases != set(snapshots):
            changed = sorted(final_databases ^ set(snapshots), key=str)
            raise BackupError('SQLite database paths changed during backup; rerun required', path=changed[0])
        before_archive = archive_inventory(inventory, plan, ganwen_root, sidecars)
        after_archive = archive_inventory(final_inventory, plan, ganwen_root, sidecars)
        if inventory_identity(before_archive) != inventory_identity(after_archive):
            raise BackupError('Project file inventory changed during backup; rerun required')
        if inventory_identity(vhost_inventory) != inventory_identity(collect_inventory([vhost])):
            raise BackupError('Vhost file inventory changed during backup; rerun required')
        # Copy fingerprints and original paths are manifest data, never printed.
        manifest['counts'] = {'registered_projects': len(plan['sites']),
                              'supplemental_project_roots': len(plan['uncovered_roots']),
                              'sqlite_databases': len(snapshots),
                              'panel_metadata_databases': len(panel_databases),
                              'restore_entries': len(manifest['restore_entries']),
                              'skipped_sockets': len(manifest['skipped_sockets'])}
        write_json(stage / 'manifest.json', manifest)
        # Put output outside the archived staging tree to avoid recursive tar.
        partial = parent / ('.' + output.name + '.partial-' + stage.name)
        phase = 'archive'
        count = pack_verified(stage, partial)
        package_sha256 = digest(partial)
        package_bytes = partial.stat().st_size
        phase = 'publish_archive'
        if output.exists() or output.is_symlink():
            raise BackupError('Output was created concurrently: ' + str(output))
        # Hard-link publication is atomic and refuses to overwrite an output
        # raced into existence.  Both paths are deliberately on the same disk.
        os.link(str(partial), str(output))
        result = {'package': str(output), 'bytes': package_bytes,
                  'sha256': package_sha256, 'files': count,
                  'counts': manifest['counts'],
                  'ganwen_counts': {key: manifest['ganwen_validation'][key]
                                    for key in ('posts', 'kv', 'translation_jobs')}}
        # Publishing a fully verified archive is the success boundary.  Cleanup
        # cannot turn an intact published archive into a false failure result.
        try:
            partial.unlink()
            shutil.rmtree(str(stage))
        except OSError:
            result['retained_private_staging'] = str(stage)
        return result
    except Exception as error:
        # No success output is published on failure.  Diagnostics are private;
        # raw exception text / SQL / CLI output never reaches public stdout.
        details = failure_details(error, phase, diagnostic_context)
        try:
            if partial.exists() and not partial.is_symlink():
                partial.unlink()
        except OSError:
            pass
        try:
            write_json(stage / 'failure.json', dict(details, format=OWNER,
                       created_at=started, success=False))
        except OSError:
            # Disk/cleanup failures must not obscure the original safe reason.
            pass
        raise BackupError('Backup failed; private diagnostics retained: ' + str(stage),
                          code=details['code'], phase=details['phase'],
                          error_type=details['error_type'], diagnostics=details.get('path')) from error


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', help='new absolute private tar.gz path (caller encrypts/uploads)')
    parser.add_argument('--dry-run', action='store_true', help='print scope paths/counts without any snapshots')
    parser.add_argument('--wwwroot', default='/www/wwwroot')
    parser.add_argument('--panel-root', default='/www/server/panel')
    parser.add_argument('--ganwen-root', default='/www/wwwroot/ganwen-go')
    args = parser.parse_args(argv)
    if not args.dry_run and not args.output:
        parser.error('--output is required unless --dry-run is used')
    # New files/private staging default private even before chmod completes.
    os.umask(0o077)
    phase = 'load_panel_metadata'
    try:
        sites = load_sites(args.panel_root)
        if args.dry_run:
            phase = 'plan_scope'
            plan = build_plan(args.wwwroot, sites)
            roots = unique_roots(plan['project_roots'] + [safe_path(args.ganwen_root, directory=True),
                                                        safe_path(args.wwwroot, directory=True)])
            inventory = collect_inventory(roots)
            safe_path(Path(args.panel_root) / 'vhost', directory=True)
            panel_databases = [safe_path(Path(args.panel_root) / 'data/db' / name, directory=False)
                               for name in PANEL_METADATA_DATABASES]
            print(json.dumps({'registered_roots': [str(path) for path in plan['registered_roots']],
                              'supplemental_roots': [str(path) for path in plan['uncovered_roots']],
                              'wwwroot_loose_files': plan['wwwroot_loose_files'],
                              'sqlite_paths': [row['path'] for row in inventory if row.get('sqlite')],
                              'panel_metadata_paths': [str(path) for path in panel_databases],
                              'ganwen_bundle': str(Path(args.ganwen_root)),
                              'vhost': str(Path(args.panel_root) / 'vhost'),
                              'counts': {'registered_projects': len(plan['sites']),
                                         'supplemental_roots': len(plan['uncovered_roots'])}},
                             ensure_ascii=False))
        else:
            phase = 'preflight'
            result = run_backup(args.output, args.wwwroot, args.panel_root, sites, args.ganwen_root)
            print(json.dumps(result, ensure_ascii=False))
        return 0
    except Exception as error:
        # Never render arbitrary SQL/public-module/CLI exception values.
        details = failure_details(error, phase, {'ganwen_roots': [Path(args.ganwen_root)],
                    'panel_roots': [Path(args.panel_root)], 'wwwroot_roots': [Path(args.wwwroot)],
                    'source_roots': [Path(args.ganwen_root), Path(args.wwwroot), Path(args.panel_root) / 'vhost']})
        print('PROJECT_BACKUP_FAILED ' + json.dumps(details), file=sys.stderr, flush=True)
        return 1


if __name__ == '__main__':
    sys.exit(main())
