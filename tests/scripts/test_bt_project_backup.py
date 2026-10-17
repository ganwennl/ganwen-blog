"""Isolated regression tests; never contact a panel, server, API or worker.

Run with: python tests/scripts/test_bt_project_backup.py -v
POSIX hosts additionally exercise real symlinks and special files.
"""

from contextlib import closing, contextmanager
import errno
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import sqlite3
import sys
import tarfile
import tempfile
import unittest
from unittest import mock


SCRIPT = (Path(__file__).resolve().parents[2] / 'scripts/backup/bt-project-backup.py')
spec = importlib.util.spec_from_file_location('bt_project_backup_under_test', SCRIPT)
backup = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = backup
spec.loader.exec_module(backup)

REJECTIONS = (ValueError, RuntimeError, OSError, sqlite3.DatabaseError)
SECRET = 'TEST-SECRET-MUST-NOT-APPEAR-829B6D'


class TemporaryFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='bt-backup-unittest-')
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name).resolve()
        self.wwwroot = self.base / 'wwwroot'
        self.wwwroot.mkdir()

    def project(self, name):
        path = self.wwwroot / name
        path.mkdir(parents=True, exist_ok=True)
        return path

    def sites(self, *paths):
        return [{'name': 'site-' + str(index), 'path': str(path)}
                for index, path in enumerate(paths)]

    def paths(self, plan, key):
        return [Path(path).resolve() for path in plan[key]]

    def sqlite(self, path):
        path.parent.mkdir(parents=True, exist_ok=True)
        with closing(sqlite3.connect(path)) as connection:
            connection.execute('CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT)')
            connection.execute('INSERT INTO entries (value) VALUES (?)', ('baseline',))
            connection.commit()
        return path

    def link(self, link, target):
        try:
            link.symlink_to(target, target_is_directory=target.is_dir())
        except (OSError, NotImplementedError) as error:
            self.skipTest('Host cannot create real symlinks: ' + str(error))
        return link


class ScopeTests(TemporaryFixture):
    def test_registered_and_unregistered_directories_are_included(self):
        registered = self.project('registered')
        uncovered = self.project('unregistered-new-project')
        (self.wwwroot / 'loose-file.txt').write_text('ordinary file', encoding='utf-8')
        plan = backup.build_plan(self.wwwroot, self.sites(registered))
        self.assertEqual(self.paths(plan, 'registered_roots'), [registered.resolve()])
        self.assertEqual(self.paths(plan, 'uncovered_roots'), [uncovered.resolve()])
        self.assertCountEqual(self.paths(plan, 'project_roots'), [registered, uncovered])

    def test_new_site_registration_is_discovered_without_a_hardcoded_list(self):
        first = self.project('first')
        added = self.project('a-new-site-2026')
        initial = backup.build_plan(self.wwwroot, self.sites(first))
        updated = backup.build_plan(self.wwwroot, self.sites(first, added))
        self.assertIn(added.resolve(), self.paths(initial, 'uncovered_roots'))
        self.assertIn(added.resolve(), self.paths(updated, 'registered_roots'))
        self.assertNotIn(added.resolve(), self.paths(updated, 'uncovered_roots'))

    def test_go_executable_registration_uses_its_project_directory(self):
        project = self.project('go-service')
        executable = project / 'service-linux-amd64'
        executable.write_bytes(b'\x7fELF\x02\x01' + b'\0' * 64)
        plan = backup.build_plan(self.wwwroot, self.sites(executable))
        self.assertEqual(self.paths(plan, 'registered_roots'), [project.resolve()])
        self.assertEqual(self.paths(plan, 'project_roots'), [project.resolve()])

    def test_duplicate_and_nested_registrations_are_archived_once(self):
        project = self.project('parent')
        child = project / 'nested-app'
        child.mkdir()
        plan = backup.build_plan(self.wwwroot, self.sites(child, project, project, child))
        self.assertEqual(self.paths(plan, 'project_roots'), [project.resolve()])

    def test_explicitly_registered_external_project_is_included(self):
        inside = self.project('inside')
        outside = self.base / 'external-project'
        outside.mkdir()
        plan = backup.build_plan(self.wwwroot, self.sites(outside))
        self.assertEqual(self.paths(plan, 'registered_roots'), [outside.resolve()])
        self.assertCountEqual(self.paths(plan, 'project_roots'), [inside, outside])

    def test_path_traversal_to_existing_external_directory_is_rejected(self):
        outside = self.base / 'outside'
        outside.mkdir()
        traversal = self.wwwroot / '..' / outside.name
        with self.assertRaises(REJECTIONS):
            backup.build_plan(self.wwwroot, self.sites(traversal))

    def test_registered_symlink_root_is_rejected(self):
        outside = self.base / 'outside'
        outside.mkdir()
        link = self.link(self.wwwroot / 'linked-site', outside)
        with self.assertRaises(REJECTIONS):
            backup.build_plan(self.wwwroot, self.sites(link))

    def test_unregistered_symlink_directory_is_rejected(self):
        target = self.base / 'outside'
        target.mkdir()
        self.link(self.wwwroot / 'linked-project', target)
        with self.assertRaises(REJECTIONS):
            backup.build_plan(self.wwwroot, [])

    def test_root_symlink_rejection_branch_runs_without_host_link_privilege(self):
        project = self.project('marked-link')
        original = Path.is_symlink
        with mock.patch.object(Path, 'is_symlink', autospec=True,
                               side_effect=lambda path: path == project or original(path)):
            with self.assertRaises(REJECTIONS):
                backup.build_plan(self.wwwroot, self.sites(project))

    def test_scope_does_not_echo_unrelated_sensitive_site_fields(self):
        project = self.project('site')
        sites = self.sites(project)
        sites[0].update(password=SECRET, api_key=SECRET,
                        connection={'mysql_password': SECRET}, description=SECRET)
        plan = backup.build_plan(self.wwwroot, sites)
        serialized = json.dumps(plan, default=str, ensure_ascii=False)
        self.assertNotIn(SECRET, serialized)
        self.assertEqual(self.paths(plan, 'project_roots'), [project.resolve()])


class SQLiteTests(TemporaryFixture):
    def test_header_detection_finds_sqlite_without_a_db_suffix(self):
        project = self.project('app')
        real = self.sqlite(project / 'state' / 'application.data')
        (project / 'pretend.db').write_text('ordinary non-database content', encoding='utf-8')
        (project / 'settings.json').write_text('{"token":"' + SECRET + '"}', encoding='utf-8')
        discovered = [Path(path).resolve() for path in backup.discover_sqlite([project])]
        self.assertEqual(discovered, [real.resolve()])

    def test_multiple_real_databases_are_discovered(self):
        first = self.project('one')
        second = self.project('two')
        expected = [self.sqlite(first / 'db.sqlite'), self.sqlite(second / 'nested' / 'state')]
        found = [Path(path).resolve() for path in backup.discover_sqlite([first, second])]
        self.assertCountEqual(found, expected)

    def test_vendor_cache_and_hidden_directories_are_not_implicitly_excluded(self):
        project = self.project('app')
        expected = [self.sqlite(project / directory / 'state')
                    for directory in ('vendor', 'cache', '.local-state')]
        found = [Path(path).resolve() for path in backup.discover_sqlite([project])]
        self.assertCountEqual(found, expected)

    def test_discovery_rejects_an_external_symlink_file(self):
        project = self.project('app')
        external = self.sqlite(self.base / 'external.sqlite')
        self.link(project / 'linked.sqlite', external)
        with self.assertRaises(REJECTIONS):
            backup.discover_sqlite([project])

    def test_discovery_rejects_an_external_symlink_directory(self):
        project = self.project('app')
        outside = self.base / 'outside'
        self.sqlite(outside / 'db.sqlite')
        self.link(project / 'external-directory', outside)
        with self.assertRaises(REJECTIONS):
            backup.discover_sqlite([project])

    def test_legacy_reparse_rejection_branch_runs_without_host_link_privilege(self):
        project = self.project('app')
        database = self.sqlite(project / 'db.sqlite')
        original = Path.is_symlink
        with mock.patch.object(Path, 'is_symlink', autospec=True,
                               side_effect=lambda path: path == database or original(path)):
            with self.assertRaises(REJECTIONS):
                backup.collect_inventory_legacy([project])

    def test_online_backup_contains_committed_wal_and_excludes_uncommitted_rows(self):
        project = self.project('app')
        source = self.sqlite(project / 'live.sqlite')
        target = self.base / 'snapshot.sqlite'
        with closing(sqlite3.connect(source)) as writer:
            self.assertEqual(writer.execute('PRAGMA journal_mode=WAL').fetchone()[0], 'wal')
            writer.execute('PRAGMA wal_autocheckpoint=0')
            writer.execute('PRAGMA wal_checkpoint(TRUNCATE)')
            writer.execute('INSERT INTO entries (value) VALUES (?)', ('wal committed',))
            writer.commit()
            self.assertGreater(Path(str(source) + '-wal').stat().st_size, 0)
            writer.execute('INSERT INTO entries (value) VALUES (?)', ('not committed',))
            backup.backup_sqlite(source, target)
            writer.rollback()
        with closing(sqlite3.connect(target.as_uri() + '?mode=ro', uri=True)) as restored:
            self.assertEqual(restored.execute('PRAGMA integrity_check').fetchall(), [('ok',)])
            self.assertEqual(restored.execute('SELECT value FROM entries ORDER BY id').fetchall(),
                             [('baseline',), ('wal committed',)])

    def test_invalid_sqlite_fails_and_does_not_leave_a_successful_snapshot(self):
        source = self.project('app') / 'broken.sqlite'
        source.write_bytes(b'SQLite format 3\0' + b'not-a-real-database' * 256)
        target = self.base / 'snapshot.sqlite'
        with self.assertRaises(REJECTIONS):
            backup.backup_sqlite(source, target)
        self.assertFalse(target.exists(), 'A failed backup must not publish a snapshot')

    def test_snapshot_never_replaces_its_live_source(self):
        source = self.sqlite(self.project('app') / 'database.sqlite')
        before = source.read_bytes()
        with self.assertRaises(REJECTIONS):
            backup.backup_sqlite(source, source)
        self.assertEqual(source.read_bytes(), before)
        with closing(sqlite3.connect(source)) as connection:
            self.assertEqual(connection.execute('PRAGMA integrity_check').fetchone(), ('ok',))

    def test_snapshot_rejects_symlink_source(self):
        project = self.project('app')
        source = self.sqlite(project / 'database.sqlite')
        link = self.link(project / 'database-link.sqlite', source)
        target = self.base / 'snapshot.sqlite'
        with self.assertRaises(REJECTIONS):
            backup.backup_sqlite(link, target)
        self.assertFalse(target.exists())


class InventoryTests(TemporaryFixture):
    def test_legacy_optional_cache_that_becomes_sqlite_and_disappears_after_read_is_required(self):
        project = self.project('native')
        target = project / 'cache/generated.php'
        target.parent.mkdir()
        target.write_text('initial ordinary cache', encoding='utf-8')
        initial = backup.collect_inventory_legacy([project])
        plan = backup.build_plan(self.wwwroot, self.sites(project))
        optional = backup.native_optional_cache_paths(initial, plan, self.base / 'ganwen')
        self.assertIn(str(target), optional)
        replacement = self.sqlite(self.base / 'replacement.sqlite')
        target.write_bytes(replacement.read_bytes())
        original_regular = backup.open_regular
        @contextmanager
        def disappearing(path):
            with original_regular(path) as stream:
                yield stream
            if Path(path) == target:
                target.unlink()
        with mock.patch.object(backup, 'open_regular', disappearing), self.assertRaises(backup.BackupError) as failed:
            backup.collect_inventory_legacy([project], transient_paths=optional)
        self.assertEqual(failed.exception.code, 'required_path_missing')
        details = backup.failure_details(failed.exception, 'final_inventory')
        self.assertEqual(details['path']['path_type'], 'sqlite_database')

    def test_inventory_keeps_paths_and_fingerprints_separate_from_file_contents(self):
        project = self.project('app')
        (project / '.env').write_text('API_KEY=' + SECRET, encoding='utf-8')
        rows = backup.collect_inventory([project])
        self.assertNotIn(SECRET, json.dumps(rows, default=str))
        self.assertEqual([row['relative'] for row in rows if row['kind'] == 'file'], ['.env'])

    def test_internal_relative_directory_link_is_recorded_without_duplicate_traversal(self):
        project = self.project('app')
        actual = project / 'actual'
        actual.mkdir()
        (actual / 'source.txt').write_text('ordinary data', encoding='utf-8')
        self.link(project / 'alias', Path('actual'))
        rows = backup.collect_inventory([project])
        links = [row for row in rows if row['kind'] == 'symlink']
        self.assertEqual([(row['relative'], row['target']) for row in links], [('alias', 'actual')])
        self.assertEqual([row['relative'] for row in rows if row['kind'] == 'file'],
                         ['actual/source.txt'])

    @unittest.skipUnless(os.name == 'posix' and hasattr(socket, 'AF_UNIX'),
                         'Real socket-file inspection requires a POSIX host')
    def test_socket_is_recorded_as_service_recreated_without_reading_it(self):
        project = self.project('app')
        endpoint = project / 'runtime.sock'
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
            try:
                listener.bind(str(endpoint))
            except OSError as error:
                self.skipTest('Host cannot bind a Unix-domain socket: ' + str(error))
            rows = backup.collect_inventory([project])
        sockets = [row for row in rows if row['kind'] == 'socket']
        self.assertEqual([row['path'] for row in sockets], [str(endpoint)])
        self.assertEqual(sockets[0]['restore'], 'recreated by the owning service')

    @unittest.skipUnless(hasattr(os, 'mkfifo'), 'Host has no POSIX FIFO support')
    def test_unsupported_fifo_is_rejected_without_blocking(self):
        project = self.project('app')
        os.mkfifo(project / 'fifo')
        with self.assertRaises(REJECTIONS):
            backup.collect_inventory([project])


@unittest.skipUnless(os.name == 'posix' and hasattr(os, 'O_NOFOLLOW') and
                         hasattr(os, 'O_DIRECTORY') and os.open in os.supports_dir_fd,
                     'Anchored descriptor inventory requires POSIX openat/O_NOFOLLOW')
class PosixInventoryTests(TemporaryFixture):
    def assert_descriptors_closed(self, descriptors):
        self.assertTrue(descriptors, 'Fixture did not observe any real file descriptors')
        for descriptor in set(descriptors):
            with self.subTest(descriptor=descriptor):
                with self.assertRaises(OSError) as closed:
                    os.fstat(descriptor)
                self.assertEqual(closed.exception.errno, errno.EBADF)

    def test_deep_inventory_validates_only_roots_and_still_reads_each_file_header(self):
        first = self.project('first-root')
        second = self.project('second-root')
        folder = first
        expected_files = []
        for level in range(8):
            folder = folder / ('level-' + str(level))
            folder.mkdir()
            for index in range(6):
                path = folder / ('ordinary-' + str(index) + '.db')
                path.write_text('ordinary non-SQLite content ' + SECRET, encoding='utf-8')
                expected_files.append(path)
        actual_databases = [self.sqlite(folder / 'actual-without-db-suffix'),
                            self.sqlite(second / 'second-state')]
        expected_files.extend(actual_databases)
        with mock.patch.object(backup, 'safe_path', wraps=backup.safe_path) as validation:
            rows = backup.collect_inventory([first, second])
        self.assertEqual(validation.call_count, 2,
                         'Fast inventory must validate each root once, never each nested file')
        self.assertCountEqual([Path(call[0][0]) for call in validation.call_args_list],
                              [first, second])
        files = [row for row in rows if row['kind'] == 'file']
        self.assertCountEqual([Path(row['path']) for row in files], expected_files)
        self.assertCountEqual([Path(row['path']) for row in files if row['sqlite']], actual_databases)
        self.assertNotIn(SECRET, json.dumps(rows, default=str))

    def test_regular_reads_are_anchored_nofollow_and_all_descriptors_close(self):
        project = self.project('app')
        nested = project / 'nested'
        nested.mkdir()
        database = self.sqlite(nested / 'actual-state')
        ordinary = nested / 'ordinary.txt'
        ordinary.write_text('ordinary fixture', encoding='utf-8')
        self.link(nested / 'relative-alias', Path('actual-state'))
        original_open = os.open
        descriptors = []
        opens = []
        def audited_open(path, flags, mode=0o777, *, dir_fd=None):
            descriptor = original_open(path, flags, mode, dir_fd=dir_fd)
            descriptors.append(descriptor)
            opens.append((os.fspath(path), flags, dir_fd))
            return descriptor
        with mock.patch.object(backup.os, 'open', side_effect=audited_open):
            rows = backup.collect_inventory_posix([project])
        files = [row for row in rows if row['kind'] == 'file']
        for row in files:
            source = Path(row['path'])
            leaf_opens = [entry for entry in opens if entry[0] == source.name]
            self.assertEqual(len(leaf_opens), 1)
            self.assertIsNotNone(leaf_opens[0][2], 'File header read escaped its directory descriptor')
            self.assertTrue(leaf_opens[0][1] & os.O_NOFOLLOW)
            info = source.stat()
            self.assertEqual(tuple(row['signature'][:2]), (info.st_dev, info.st_ino))
        self.assertTrue(all(flags & os.O_NOFOLLOW for path, flags, descriptor in opens))
        self.assertCountEqual([Path(row['path']) for row in files], [database, ordinary])
        self.assertEqual([(row['relative'], row['target']) for row in rows
                          if row['kind'] == 'symlink'], [('nested/relative-alias', 'actual-state')])
        self.assert_descriptors_closed(descriptors)

    def test_entry_replaced_with_external_link_or_fifo_is_rejected_without_fd_leaks(self):
        original_open = os.open
        for kind in ('directory', 'file', 'fifo'):
            with self.subTest(kind=kind):
                project = self.project('race-' + kind)
                outside = self.base / ('outside-' + kind)
                victim = project / 'victim'
                if kind == 'directory':
                    victim.mkdir()
                    (victim / 'ordinary.txt').write_text('original', encoding='utf-8')
                    outside.mkdir()
                    (outside / 'private.txt').write_text(SECRET, encoding='utf-8')
                else:
                    victim.write_text('original', encoding='utf-8')
                    outside.write_text(SECRET, encoding='utf-8')
                descriptors = []
                replaced = []
                def racing_open(path, flags, mode=0o777, *, dir_fd=None):
                    if os.fspath(path) == victim.name and dir_fd is not None and not replaced:
                        victim.rename(project / 'original-victim')
                        if kind == 'fifo':
                            self.assertTrue(flags & os.O_NONBLOCK,
                                            'Stat-to-FIFO replacement must never cause a blocking open')
                            os.mkfifo(victim)
                        else:
                            victim.symlink_to(outside, target_is_directory=(kind == 'directory'))
                        replaced.append(True)
                    descriptor = original_open(path, flags, mode, dir_fd=dir_fd)
                    descriptors.append(descriptor)
                    return descriptor
                with mock.patch.object(backup.os, 'open', side_effect=racing_open):
                    with self.assertRaises(REJECTIONS):
                        backup.collect_inventory_posix([project])
                self.assertEqual(replaced, [True], 'Race was not injected at an anchored entry open')
                self.assert_descriptors_closed(descriptors)

    def test_regular_inode_substitution_is_rejected_and_descriptors_close_on_failure(self):
        project = self.project('inode-race')
        victim = project / 'victim.data'
        victim.write_text('original same-size-data', encoding='utf-8')
        original_inode = victim.stat().st_ino
        original_open = os.open
        descriptors = []
        replaced = []
        def racing_open(path, flags, mode=0o777, *, dir_fd=None):
            if os.fspath(path) == victim.name and dir_fd is not None and not replaced:
                victim.rename(project / 'original-victim')
                victim.write_text('replaced same-size-data', encoding='utf-8')
                self.assertNotEqual(victim.stat().st_ino, original_inode)
                replaced.append(True)
            descriptor = original_open(path, flags, mode, dir_fd=dir_fd)
            descriptors.append(descriptor)
            return descriptor
        with mock.patch.object(backup.os, 'open', side_effect=racing_open):
            with self.assertRaises(REJECTIONS):
                backup.collect_inventory_posix([project])
        self.assertEqual(replaced, [True], 'Regular inode replacement race was not injected')
        self.assert_descriptors_closed(descriptors)


class CopyTests(TemporaryFixture):
    class ReadMutation:
        def __init__(self, stream, mutation):
            self.stream = stream
            self.mutation = mutation

        def __enter__(self):
            self.stream.__enter__()
            return self

        def __exit__(self, *exception):
            return self.stream.__exit__(*exception)

        def __getattr__(self, name):
            return getattr(self.stream, name)

        def read(self, size=-1):
            block = self.stream.read(size)
            if self.mutation is not None:
                mutation = self.mutation
                self.mutation = None
                mutation()
            return block

    def mutating_opener(self, source, mutation):
        original_open = backup.open_regular
        def opener(path):
            stream = original_open(path)
            if Path(path) == source:
                return self.ReadMutation(stream, mutation)
            return stream
        return opener

    def test_appending_log_copies_exact_opened_prefix_and_records_its_size(self):
        source = self.project('app') / 'runtime.log'
        prefix = b'original-log-entry\n' * 100000
        appended = b'appended-during-copy\n'
        source.write_bytes(prefix)
        destination = self.base / 'stage' / 'runtime.log'
        mutations = []
        def append_after_first_read():
            with source.open('ab') as writer:
                writer.write(appended)
            mutations.append(True)
        with mock.patch.object(backup, 'open_regular',
                side_effect=self.mutating_opener(source, append_after_first_read)):
            record = backup.copy_regular(source, destination)
        self.assertEqual(mutations, [True])
        self.assertEqual(source.read_bytes(), prefix + appended)
        self.assertEqual(destination.read_bytes(), prefix)
        self.assertEqual(record['bytes'], len(prefix))
        self.assertEqual(record['sha256'], hashlib.sha256(prefix).hexdigest())
        self.assertEqual(record['snapshot_mode'], 'snapshot-prefix')
        self.assertEqual(record['snapshot_prefix_bytes'], len(prefix))

    def test_truncated_or_replaced_log_and_appended_regular_file_are_rejected(self):
        for action in ('truncate-log', 'append-ordinary', 'replace-log'):
            with self.subTest(action=action):
                project = self.project(action)
                source = project / ('runtime.txt' if action == 'append-ordinary' else 'runtime.log')
                original_bytes = b'original-entry\n' * 100000
                source.write_bytes(original_bytes)
                destination = self.base / ('stage-' + action) / source.name
                mutations = []
                patches = []
                original_lstat = Path.lstat
                def mutate_after_first_read():
                    if action == 'truncate-log':
                        source.write_bytes(b'truncated')
                    elif action == 'append-ordinary':
                        with source.open('ab') as writer:
                            writer.write(b'appended-data\n')
                    elif os.name == 'posix':
                        source.rename(project / 'original-open-inode')
                        source.write_bytes(original_bytes)
                    else:
                        # Windows denies renaming an open file.  Inject the
                        # replacement inode reported by final lstat while the
                        # held descriptor continues to identify the old inode.
                        fields = list(source.lstat())
                        fields[1] += 1
                        replacement = os.stat_result(fields)
                        patch = mock.patch.object(Path, 'lstat', autospec=True,
                            side_effect=lambda path, *args, **kwargs: replacement if path == source
                            else original_lstat(path, *args, **kwargs))
                        patch.start()
                        patches.append(patch)
                    mutations.append(True)
                try:
                    with mock.patch.object(backup, 'open_regular',
                            side_effect=self.mutating_opener(source, mutate_after_first_read)):
                        with self.assertRaises(backup.BackupError):
                            backup.copy_regular(source, destination)
                    self.assertEqual(mutations, [True], 'Copy mutation did not complete')
                finally:
                    for patch in reversed(patches):
                        patch.stop()


class RunBackupTests(TemporaryFixture):
    def setUp(self):
        super().setUp()
        self.ganwen = self.project('ganwen-go')
        self.ganwen_data = self.ganwen / 'data'
        self.ganwen_data.mkdir()
        (self.ganwen / 'ganwen').write_bytes(b'isolated-fixture-never-executed')
        (self.ganwen_data / 'runtime.env').write_text('API_KEY=' + SECRET, encoding='utf-8')
        self.key = b'isolated-fixture-key-829b6d-000000'
        (self.ganwen_data / 'translation.key').write_bytes(self.key)
        self.document = {
            'format': 'ganwen-blog-database', 'version': 1,
            'encryption_key_sha256': hashlib.sha256(self.key).hexdigest(),
            'posts': [{'id': 1447, 'title': 'fixture post', 'content': SECRET}],
            'values': [{'key': 'private_provider_key', 'value': SECRET}],
            'translation_jobs': [dict(zip(backup.JOB_FIELDS,
                (1, 1447, 'en', 'source-hash', 'pending', 0, '', 0, 'baseline-hash')))],
        }
        self.write_ganwen_database(self.ganwen_data / 'blog.db', self.document)
        self.registered = self.project('native-registered')
        (self.registered / 'private-config.env').write_text(SECRET, encoding='utf-8')
        self.registered_sqlite = self.sqlite(self.registered / 'state.data')
        self.uncovered = self.project('unregistered')
        (self.uncovered / '.env').write_text('PRIVATE=' + SECRET, encoding='utf-8')
        (self.uncovered / 'original-private.log').write_text(SECRET, encoding='utf-8')
        (self.wwwroot / 'loose-private.log').write_text(SECRET, encoding='utf-8')
        self.live_sqlite = self.sqlite(self.uncovered / 'live.sqlite')
        self.panel = self.base / 'panel'
        self.vhost = self.panel / 'vhost' / 'nginx'
        self.vhost.mkdir(parents=True)
        (self.vhost / 'site.conf').write_text('fixture config ' + SECRET, encoding='utf-8')
        self.panel_databases = [self.sqlite(self.panel / 'data' / 'db' / name)
                                for name in ('site.db', 'database.db', 'crontab.db')]
        self.site_records = self.sites(self.ganwen / 'ganwen', self.registered)
        self.site_records[0].update(api_key=SECRET, password=SECRET)
        self.commands = []
        self.output = self.base / 'backup.tar.gz'

    def write_ganwen_database(self, path, document):
        with closing(sqlite3.connect(path)) as connection:
            connection.execute('CREATE TABLE posts (id INTEGER PRIMARY KEY, document TEXT)')
            connection.executemany('INSERT INTO posts VALUES (?,?)',
                [(post['id'], json.dumps(post, ensure_ascii=False)) for post in document['posts']])
            connection.execute('CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT)')
            connection.executemany('INSERT INTO kv VALUES (?,?)',
                [(row['key'], row['value']) for row in document['values']])
            columns = ','.join(field + ' ' + ('INTEGER' if field in
                ('id', 'post_id', 'attempts', 'next_attempt_at') else 'TEXT')
                for field in backup.JOB_FIELDS)
            connection.execute('CREATE TABLE translation_jobs (' + columns + ')')
            connection.executemany('INSERT INTO translation_jobs VALUES (' +
                ','.join('?' for field in backup.JOB_FIELDS) + ')',
                [tuple(row[field] for field in backup.JOB_FIELDS)
                 for row in document['translation_jobs']])
            analytics = document.get('analytics')
            if analytics is not None:
                for field, table, columns in (
                        ('aggregates', 'analytics_aggregates', ('date', 'kind', 'label', 'post_id', 'pv', 'uv')),
                        ('visitors', 'analytics_visitors', ('date', 'hash')),
                        ('events', 'analytics_events', ('date', 'hash'))):
                    sql_columns = ['id_hash' if name == 'hash' else name for name in columns]
                    connection.execute('CREATE TABLE ' + table + '(' + ','.join(
                        name + (' INTEGER' if name in ('post_id', 'pv', 'uv') else ' TEXT')
                        for name in sql_columns) + ')')
                    connection.executemany('INSERT INTO ' + table + ' VALUES (' +
                        ','.join('?' for _ in columns) + ')',
                        [tuple(row[name] for name in columns) for row in analytics[field]])
            connection.commit()

    def runner(self, argv, logpath):
        self.commands.append(argv[1])
        Path(logpath).write_text('private fixture log ' + SECRET, encoding='utf-8')
        document_path = Path(argv[argv.index('--file') + 1])
        if argv[1] == 'database-export':
            document_path.write_text(json.dumps(self.document), encoding='utf-8')
            Path(str(document_path) + '.translation.key').write_bytes(self.key)
        elif argv[1] == 'database-import':
            destination = Path(argv[argv.index('--data') + 1])
            document = json.loads(document_path.read_text(encoding='utf-8'))
            self.write_ganwen_database(destination / 'blog.db', document)
            (destination / 'translation.key').write_bytes(self.key)
        else:
            self.fail('Unexpected command: ' + argv[1])

    def execute(self, runner=None, output=None):
        return backup.run_backup(output or self.output, self.wwwroot, self.panel,
                                 self.site_records, self.ganwen, runner or self.runner)

    def test_analytics_restoration_is_verified_and_missing_counts_fail(self):
        document = dict(self.document)
        document['analytics'] = {
            'aggregates': [{'date': '2026-10-08', 'kind': 'total', 'label': 'all',
                            'post_id': 0, 'pv': 17, 'uv': 9}],
            'visitors': [{'date': '2026-10-08', 'hash': 'a' * 64}],
            'events': [{'date': '2026-10-08', 'hash': 'b' * 64}],
        }
        destination = self.base / 'analytics-isolated-restore'
        destination.mkdir()
        self.write_ganwen_database(destination / 'blog.db', document)
        (destination / 'translation.key').write_bytes(self.key)
        self.assertEqual(backup.verify_ganwen_restore(destination, document)['analytics_rows'], 3)
        with closing(sqlite3.connect(destination / 'blog.db')) as connection:
            connection.execute('UPDATE analytics_aggregates SET pv=16')
            connection.commit()
        with self.assertRaisesRegex(backup.BackupError, 'analytics differ'):
            backup.verify_ganwen_restore(destination, document)

    def manifest(self):
        with tarfile.open(self.output, 'r:gz') as archive:
            return json.loads(archive.extractfile('manifest.json').read())

    def failure_records(self):
        return [json.loads(path.read_text(encoding='utf-8'))
                for path in self.base.glob('.bt-project-stage-*/failure.json')]

    def test_complete_fixture_proves_wal_snapshot_keys_and_native_scope(self):
        with closing(sqlite3.connect(self.live_sqlite)) as writer, \
                closing(sqlite3.connect(self.panel_databases[0])) as panel_writer:
            writer.execute('PRAGMA journal_mode=WAL')
            writer.execute('PRAGMA wal_autocheckpoint=0')
            writer.execute('PRAGMA wal_checkpoint(TRUNCATE)')
            writer.execute('INSERT INTO entries (value) VALUES (?)', ('committed WAL row',))
            writer.commit()
            self.assertGreater(Path(str(self.live_sqlite) + '-wal').stat().st_size, 0)
            panel_writer.execute('PRAGMA journal_mode=WAL')
            panel_writer.execute('PRAGMA wal_autocheckpoint=0')
            panel_writer.execute('PRAGMA wal_checkpoint(TRUNCATE)')
            panel_writer.execute('INSERT INTO entries (value) VALUES (?)', ('panel committed WAL row',))
            panel_writer.commit()
            self.assertGreater(Path(str(self.panel_databases[0]) + '-wal').stat().st_size, 0)
            result = self.execute()
        self.assertEqual(self.commands, ['database-export', 'database-import'])
        self.assertEqual(result['sha256'], hashlib.sha256(self.output.read_bytes()).hexdigest())
        self.assertEqual(result['bytes'], self.output.stat().st_size)
        self.assertEqual(result['ganwen_counts'], {'posts': 1, 'kv': 1, 'translation_jobs': 1})
        manifest = self.manifest()
        self.assertEqual(manifest['native_all_required'], {'websites': True, 'mysql_databases': True})
        self.assertEqual(manifest['counts']['sqlite_databases'], 3)
        self.assertEqual(manifest['counts']['panel_metadata_databases'], 3)
        self.assertCountEqual([row['restore_path'] for row in manifest['panel_metadata_snapshots']],
                              [str(path) for path in self.panel_databases])
        self.assertFalse(manifest['ganwen_validation']['worker_started'])
        self.assertNotIn(SECRET, json.dumps(manifest))
        with tarfile.open(self.output, 'r:gz') as archive:
            names = archive.getnames()
            self.assertNotIn(SECRET, archive.extractfile('metadata/sites.json').read().decode('utf-8'))
            self.assertEqual(archive.extractfile('ganwen/database.json.translation.key').read(), self.key)
            self.assertNotIn('export-private.log', names)
            self.assertNotIn('import-private.log', names)
            self.assertTrue(any(name.endswith('/unregistered/original-private.log') for name in names))
            self.assertTrue(any(name.endswith('/wwwroot/loose-private.log') for name in names))
            self.assertFalse(any('isolated-restore' in name for name in names))
            self.assertFalse(any(name.endswith(('-wal', '-shm', '-journal')) for name in names))
            self.assertFalse(any(name.endswith('/native-registered/private-config.env') for name in names))
            self.assertTrue(any(name.endswith('/unregistered/.env') for name in names))
            self.assertTrue(any(name.endswith('/ganwen-go/data/runtime.env') for name in names))
            source = next(row for row in manifest['sqlite_snapshots']
                          if row['restore_path'] == str(self.live_sqlite))
            restored = self.base / 'restored-snapshot.sqlite'
            restored.write_bytes(archive.extractfile(source['archive']).read())
            panel_source = next(row for row in manifest['panel_metadata_snapshots']
                                if row['restore_path'] == str(self.panel_databases[0]))
            restored_panel = self.base / 'restored-panel.sqlite'
            restored_panel.write_bytes(archive.extractfile(panel_source['archive']).read())
        with closing(sqlite3.connect(restored)) as connection:
            self.assertEqual(connection.execute('PRAGMA integrity_check').fetchone(), ('ok',))
            self.assertEqual(connection.execute('SELECT value FROM entries ORDER BY id').fetchall(),
                             [('baseline',), ('committed WAL row',)])
        with closing(sqlite3.connect(restored_panel)) as connection:
            self.assertEqual(connection.execute('PRAGMA integrity_check').fetchone(), ('ok',))
            self.assertEqual(connection.execute('SELECT value FROM entries ORDER BY id').fetchall(),
                             [('baseline',), ('panel committed WAL row',)])
        self.assertFalse(list(self.base.glob('.bt-project-stage-*')))

    def test_runner_failure_publishes_no_package_and_keeps_sanitized_failure(self):
        def failed_runner(argv, logpath):
            Path(logpath).write_text(SECRET, encoding='utf-8')
            raise RuntimeError('sensitive exception ' + SECRET)
        with self.assertRaises(backup.BackupError) as failed:
            self.execute(runner=failed_runner)
        self.assertNotIn(SECRET, str(failed.exception))
        self.assertFalse(self.output.exists())
        self.assertFalse(list(self.base.glob('.*.partial-*')))
        failures = self.failure_records()
        self.assertEqual(len(failures), 1)
        self.assertFalse(failures[0]['success'])
        self.assertEqual(failures[0]['phase'], 'ganwen_export_restore')
        self.assertEqual(failures[0]['code'], 'ganwen_export_restore_failed')
        self.assertEqual(failed.exception.phase, failures[0]['phase'])
        self.assertEqual(failed.exception.code, failures[0]['code'])
        self.assertNotIn(SECRET, json.dumps(failures))

    def test_missing_export_sidecar_blocks_publication(self):
        def without_sidecar(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                Path(argv[argv.index('--file') + 1] + '.translation.key').unlink()
        with self.assertRaises(backup.BackupError):
            self.execute(runner=without_sidecar)
        self.assertFalse(self.output.exists())
        self.assertEqual(self.commands, ['database-export'])
        self.assertFalse(self.failure_records()[0]['success'])
        self.assertEqual(self.failure_records()[0]['code'], 'ganwen_key_sidecar_failed')

    def test_partial_cleanup_failure_does_not_hide_archive_reason_or_diagnostics(self):
        original_unlink = Path.unlink
        def failed_pack(stage, output):
            Path(output).write_bytes(b'partial archive')
            raise RuntimeError(SECRET)
        def unlink(path, *args, **kwargs):
            if '.partial-' in path.name:
                raise PermissionError(SECRET)
            return original_unlink(path, *args, **kwargs)
        with mock.patch.object(backup, 'pack_verified', side_effect=failed_pack), \
                mock.patch.object(Path, 'unlink', unlink), self.assertRaises(backup.BackupError) as failed:
            self.execute()
        record = self.failure_records()[0]
        self.assertEqual(record['phase'], 'archive')
        self.assertEqual(record['error_type'], 'RuntimeError')
        self.assertEqual(failed.exception.error_type, 'RuntimeError')
        self.assertNotIn(SECRET, json.dumps(record) + str(failed.exception))

    def test_restore_content_mismatch_blocks_publication(self):
        def mismatched_import(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-import':
                database = Path(argv[argv.index('--data') + 1]) / 'blog.db'
                with closing(sqlite3.connect(database)) as connection:
                    connection.execute('DELETE FROM posts')
                    connection.commit()
        with self.assertRaises(backup.BackupError):
            self.execute(runner=mismatched_import)
        self.assertFalse(self.output.exists())
        self.assertFalse(self.failure_records()[0]['success'])
        self.assertEqual(self.failure_records()[0]['code'], 'ganwen_posts_restore_mismatch')

    def test_new_source_file_during_export_blocks_publication(self):
        def changing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                (self.uncovered / 'appeared-during-backup.txt').write_text('new source', encoding='utf-8')
        with self.assertRaises(backup.BackupError):
            self.execute(runner=changing_runner)
        self.assertFalse(self.output.exists())
        self.assertFalse(self.failure_records()[0]['success'])
        self.assertEqual(self.failure_records()[0]['code'], 'project_inventory_changed')
        self.assertEqual(self.failure_records()[0]['phase'], 'final_inventory')

    def test_new_project_during_export_blocks_publication(self):
        def changing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                self.project('appeared-during-backup')
        with self.assertRaises(backup.BackupError):
            self.execute(runner=changing_runner)
        self.assertFalse(self.output.exists())
        self.assertFalse(self.failure_records()[0]['success'])

    def test_registered_cache_creation_and_deletion_do_not_block_supplemental_backup(self):
        parent = self.project('partially-native-cache')
        nested = parent / 'registered-child'
        nested.mkdir()
        self.site_records.extend(self.sites(nested))
        (parent / 'supplemental-sibling.txt').write_text('preserve sibling', encoding='utf-8')
        cache_roots = [self.registered / 'cache', nested / 'cache']
        for cache in cache_roots:
            cache.mkdir()
            (cache / 'deleted-during-export.txt').write_text('old cache', encoding='utf-8')
        def cache_churn_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                for cache in cache_roots:
                    (cache / 'deleted-during-export.txt').unlink()
                    (cache / 'created-during-export.txt').write_text('new cache', encoding='utf-8')
        result = self.execute(runner=cache_churn_runner)
        self.assertEqual(result['counts']['sqlite_databases'], 3)
        self.assertTrue(self.output.exists())
        self.assertEqual(self.failure_records(), [])
        with tarfile.open(self.output, 'r:gz') as archive:
            names = archive.getnames()
            self.assertFalse(any(name.endswith('/deleted-during-export.txt') or
                                 name.endswith('/created-during-export.txt') for name in names))
            self.assertTrue(any(name.endswith('/partially-native-cache/supplemental-sibling.txt')
                                for name in names))

    def delete_during_final_inventory(self, target):
        """Remove a listed file immediately before its header-open, on both walkers."""
        original_inventory, original_open, original_regular = backup.collect_inventory, os.open, backup.open_regular
        scans, removed = [0], [False]
        def inventory(*args, **kwargs):
            scans[0] += 1
            return original_inventory(*args, **kwargs)
        def remove():
            if scans[0] == 3 and not removed[0]:
                target.unlink()
                removed[0] = True
        def anchored_open(path, flags, *args, **kwargs):
            if str(path) == target.name and kwargs.get('dir_fd') is not None:
                remove()
            return original_open(path, flags, *args, **kwargs)
        def regular(path):
            if Path(path) == target:
                remove()
            return original_regular(path)
        with mock.patch.object(backup, 'collect_inventory', side_effect=inventory), \
                mock.patch.object(os, 'open', side_effect=anchored_open), \
                mock.patch.object(backup, 'open_regular', side_effect=regular):
            # Preserve capability detection for the mocked os.open so Linux
            # actually exercises the descriptor-relative inventory walker.
            with mock.patch.object(os, 'supports_dir_fd', os.supports_dir_fd | {os.open}):
                result = self.execute()
        self.assertTrue(removed[0])
        return result

    def test_native_non_sqlite_cache_disappearing_between_stat_and_open_is_optional(self):
        target = self.registered / 'storage/framework/views' / (SECRET + '.php')
        target.parent.mkdir(parents=True)
        target.write_text('generated cache', encoding='utf-8')
        result = self.delete_during_final_inventory(target)
        self.assertTrue(self.output.exists())
        self.assertEqual(result['counts']['sqlite_databases'], 3)
        self.assertEqual(self.failure_records(), [])

    def test_native_configuration_disappearing_between_stat_and_open_is_required(self):
        target = self.registered / 'cache' / (SECRET + '.env')
        target.parent.mkdir()
        target.write_text(SECRET, encoding='utf-8')
        with self.assertRaises(backup.BackupError):
            self.delete_during_final_inventory(target)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['code'], 'required_path_missing')
        self.assertEqual(record['path']['source'], 'native_registered_project')
        self.assertEqual(record['path']['path_type'], 'configuration')
        self.assertEqual(record['path']['path_sha256'], hashlib.sha256(os.fsencode(str(target))).hexdigest())
        self.assertNotIn(SECRET, json.dumps(record))

    def test_native_original_media_disappearing_between_stat_and_open_is_required(self):
        target = self.registered / 'cache' / (SECRET + '.mp4')
        target.parent.mkdir()
        target.write_bytes(b'original media fixture')
        with self.assertRaises(backup.BackupError):
            self.delete_during_final_inventory(target)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['path']['path_type'], 'original_media')
        self.assertNotIn(SECRET, json.dumps(record))

    def test_uncovered_cache_disappearing_between_stat_and_open_is_required(self):
        target = self.uncovered / 'cache' / (SECRET + '.php')
        target.parent.mkdir()
        target.write_text('cache owned by supplement', encoding='utf-8')
        with self.assertRaises(backup.BackupError):
            self.delete_during_final_inventory(target)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['path']['source'], 'supplemental_project')
        self.assertEqual(record['path']['path_type'], 'runtime_cache')
        self.assertNotIn(SECRET, json.dumps(record))

    def test_native_original_media_deleted_before_final_listing_is_required(self):
        target = self.registered / (SECRET + '.jpg')
        target.write_bytes(b'original media fixture')
        def removing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                target.unlink()
        with self.assertRaises(backup.BackupError):
            self.execute(runner=removing_runner)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['code'], 'required_path_missing')
        self.assertEqual(record['path']['path_type'], 'original_media')
        self.assertNotIn(SECRET, json.dumps(record))

    def test_extensionless_original_upload_deleted_before_final_listing_is_required(self):
        target = self.registered / 'uploads' / SECRET
        target.parent.mkdir()
        target.write_bytes(b'original upload fixture')
        def removing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                target.unlink()
        with self.assertRaises(backup.BackupError):
            self.execute(runner=removing_runner)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['code'], 'required_path_missing')
        self.assertEqual(record['path']['path_type'], 'original_media')
        self.assertNotIn(SECRET, json.dumps(record))

    def test_registered_source_root_missing_before_final_plan_is_required(self):
        target = self.project('another-native-root')
        self.site_records.extend(self.sites(target))
        def removing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                target.rmdir()
        with self.assertRaises(backup.BackupError):
            self.execute(runner=removing_runner)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['code'], 'required_path_missing')
        self.assertEqual(record['path']['path_type'], 'source_root')

    def test_registered_source_root_deleted_after_final_plan_is_required(self):
        target = self.project('another-native-root')
        self.site_records.extend(self.sites(target))
        original_inventory, scans = backup.collect_inventory, [0]
        def inventory(*args, **kwargs):
            scans[0] += 1
            if scans[0] == 3:
                target.rmdir()
            return original_inventory(*args, **kwargs)
        with mock.patch.object(backup, 'collect_inventory', side_effect=inventory), self.assertRaises(backup.BackupError):
            self.execute()
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['code'], 'required_path_missing')
        self.assertEqual(record['path']['path_type'], 'source_root')

    def test_ganwen_cache_and_known_sqlite_never_become_optional(self):
        paths = [self.ganwen / 'cache/generated.php', self.registered / 'cache/state.sqlite']
        for path in paths:
            path.parent.mkdir(exist_ok=True)
        paths[0].write_text('bundle cache is part of full restore', encoding='utf-8')
        self.sqlite(paths[1])
        plan = backup.build_plan(self.wwwroot, self.site_records)
        inventory = backup.collect_inventory([self.wwwroot])
        optional = backup.native_optional_cache_paths(inventory, plan, self.ganwen)
        self.assertTrue(all(str(path) not in optional for path in paths))

    def test_native_sqlite_in_cache_disappearing_between_stat_and_open_is_required(self):
        target = self.sqlite(self.registered / 'cache' / (SECRET + '.sqlite'))
        with self.assertRaises(backup.BackupError):
            self.delete_during_final_inventory(target)
        record = self.failure_records()[0]
        self.assertFalse(self.output.exists())
        self.assertEqual(record['path']['path_type'], 'sqlite_database')
        self.assertNotIn(SECRET, json.dumps(record))

    def test_real_sqlite_sidecar_creation_and_deletion_do_not_block_or_enter_archive(self):
        sources = [self.registered_sqlite, self.ganwen_data / 'blog.db']
        for action in ('create', 'delete'):
            with self.subTest(action=action):
                self.output = self.base / ('sidecars-' + action + '.tar.gz')
                writers = []
                def open_real_wal_writers():
                    for source in sources:
                        writer = sqlite3.connect(source)
                        writers.append(writer)
                        writer.execute('PRAGMA journal_mode=WAL')
                        writer.execute('PRAGMA wal_autocheckpoint=0')
                        writer.execute('PRAGMA user_version=' + ('1' if action == 'create' else '2'))
                        writer.commit()
                        self.assertGreater(Path(str(source) + '-wal').stat().st_size, 0)
                        self.assertTrue(Path(str(source) + '-shm').exists())
                def close_real_wal_writers():
                    while writers:
                        writers.pop().close()
                if action == 'delete':
                    open_real_wal_writers()
                else:
                    self.assertFalse(any(Path(str(source) + '-wal').exists() for source in sources))
                def sidecar_churn_runner(argv, logpath):
                    self.runner(argv, logpath)
                    if argv[1] == 'database-export':
                        if action == 'create':
                            open_real_wal_writers()
                        else:
                            close_real_wal_writers()
                            self.assertFalse(any(Path(str(source) + '-wal').exists()
                                                 for source in sources))
                try:
                    result = self.execute(runner=sidecar_churn_runner)
                    self.assertEqual(result['counts']['sqlite_databases'], 3)
                    self.assertTrue(self.output.exists())
                    self.assertEqual(self.failure_records(), [])
                    with tarfile.open(self.output, 'r:gz') as archive:
                        self.assertFalse(any(name.endswith(('-wal', '-shm', '-journal'))
                                             for name in archive.getnames()))
                finally:
                    close_real_wal_writers()

    def test_new_real_sqlite_inside_registered_cache_still_blocks_publication(self):
        cache = self.registered / 'cache'
        cache.mkdir()
        def new_database_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-export':
                self.sqlite(cache / 'new-database-without-db-suffix')
        with self.assertRaises(backup.BackupError):
            self.execute(runner=new_database_runner)
        self.assertFalse(self.output.exists())
        self.assertFalse(list(self.base.glob('.*.partial-*')))
        self.assertFalse(self.failure_records()[0]['success'])

    def test_nested_registered_project_preserves_unregistered_siblings_without_duplicates(self):
        parent = self.project('partially-registered')
        nested = parent / 'native-child'
        nested.mkdir()
        (parent / 'unregistered-sibling.txt').write_text('preserve this', encoding='utf-8')
        (nested / 'native-covered.txt').write_text('native ALL task owns this', encoding='utf-8')
        database = self.sqlite(nested / 'independent-sqlite')
        self.site_records.extend(self.sites(nested))
        self.execute()
        manifest = self.manifest()
        self.assertEqual(manifest['excluded_registered_subroots'][str(parent)], [str(nested)])
        self.assertEqual(sum(row['restore_path'] == str(database)
                             for row in manifest['sqlite_snapshots']), 1)
        with tarfile.open(self.output, 'r:gz') as archive:
            names = archive.getnames()
            self.assertTrue(any(name.endswith('/partially-registered/unregistered-sibling.txt')
                                for name in names))
            self.assertFalse(any(name.endswith('/native-child/native-covered.txt') for name in names))

    def test_concurrent_output_is_preserved_and_partial_package_is_removed(self):
        raced_content = b'another-run-created-this-file'
        def racing_runner(argv, logpath):
            self.runner(argv, logpath)
            if argv[1] == 'database-import':
                self.output.write_bytes(raced_content)
        with self.assertRaises(backup.BackupError):
            self.execute(runner=racing_runner)
        self.assertEqual(self.output.read_bytes(), raced_content)
        self.assertFalse(list(self.base.glob('.*.partial-*')))
        self.assertFalse(self.failure_records()[0]['success'])

    def test_existing_output_is_not_overwritten(self):
        self.output.write_bytes(b'previous-owned-or-foreign-output')
        with self.assertRaises(backup.BackupError):
            self.execute()
        self.assertEqual(self.output.read_bytes(), b'previous-owned-or-foreign-output')
        self.assertEqual(self.commands, [])

    def test_output_inside_source_tree_is_rejected_before_any_cli_command(self):
        target = self.uncovered / 'backup.tar.gz'
        with self.assertRaises(backup.BackupError):
            self.execute(output=target)
        self.assertFalse(target.exists())
        self.assertEqual(self.commands, [])

    def test_main_renders_untrusted_failure_without_secret_exception_content(self):
        stderr = io.StringIO()
        stdout = io.StringIO()
        previous_mask = os.umask(0o077)
        self.addCleanup(os.umask, previous_mask)
        with mock.patch.object(backup, 'load_sites', side_effect=RuntimeError(SECRET)), \
                mock.patch('sys.stderr', stderr), mock.patch('sys.stdout', stdout):
            code = backup.main(['--dry-run'])
        self.assertEqual(code, 1)
        self.assertEqual(stdout.getvalue(), '')
        self.assertNotIn(SECRET, stderr.getvalue())
        self.assertIn('RuntimeError', stderr.getvalue())
        self.assertIn('load_panel_metadata_failed', stderr.getvalue())
        self.assertIn('PROJECT_BACKUP_FAILED', stderr.getvalue())

    def test_main_redacts_backup_error_message_and_unknown_missing_filename(self):
        for error in (backup.BackupError(SECRET, path=self.base / SECRET),
                      FileNotFoundError(errno.ENOENT, SECRET, str(self.base / SECRET))):
            with self.subTest(error_type=type(error).__name__):
                stderr = io.StringIO()
                with mock.patch.object(backup, 'load_sites', side_effect=error), mock.patch('sys.stderr', stderr):
                    self.assertEqual(backup.main(['--dry-run']), 1)
                self.assertNotIn(SECRET, stderr.getvalue())
                self.assertIn('path_sha256', stderr.getvalue())

    def test_dry_run_reports_scope_without_snapshots_cli_calls_or_secret_values(self):
        stdout = io.StringIO()
        previous_mask = os.umask(0o077)
        self.addCleanup(os.umask, previous_mask)
        with mock.patch.object(backup, 'load_sites', return_value=self.site_records), \
                mock.patch.object(backup, 'backup_sqlite') as snapshots, \
                mock.patch.object(backup, 'safe_cli') as cli, mock.patch('sys.stdout', stdout):
            code = backup.main(['--dry-run', '--wwwroot', str(self.wwwroot),
                                '--panel-root', str(self.panel), '--ganwen-root', str(self.ganwen)])
        self.assertEqual(code, 0)
        snapshots.assert_not_called()
        cli.assert_not_called()
        self.assertNotIn(SECRET, stdout.getvalue())
        scope = json.loads(stdout.getvalue())
        self.assertCountEqual(scope['registered_roots'], [str(self.ganwen), str(self.registered)])
        self.assertEqual(scope['supplemental_roots'], [str(self.uncovered)])
        self.assertEqual(len(scope['sqlite_paths']), 3)
        self.assertCountEqual(scope['panel_metadata_paths'], [str(path) for path in self.panel_databases])
        self.assertFalse(self.output.exists())


if __name__ == '__main__':
    unittest.main()
