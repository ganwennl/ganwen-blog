"""Real CLI restore/backup/package regression tests using only temporary data."""

from contextlib import closing, redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent.parent


def load_script(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


backup = load_script('restore_backup_under_test', ROOT / 'scripts/bt-project-backup.py')


class DatabaseRestoreConsumersTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not shutil.which('go'):
            raise unittest.SkipTest('Go is required for the real isolated CLI roundtrip')
        cls.build = tempfile.TemporaryDirectory(prefix='ganwen-restore-cli-')
        cls.addClassCleanup(cls.build.cleanup)
        cls.cli = Path(cls.build.name) / ('ganwen.exe' if os.name == 'nt' else 'ganwen')
        cls.environment = {k: v for k, v in os.environ.items() if not k.startswith('GBLOG_')}
        subprocess.run(['go', 'build', '-o', str(cls.cli), './cmd/ganwen'],
                       cwd=ROOT, env=cls.environment, check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='ganwen-restore-consumers-')
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.root = self.base / 'ganwen-go'
        self.data = self.root / 'data'
        self.data.mkdir(parents=True)
        shutil.copy2(self.cli, self.root / 'ganwen')
        document = {
            'format': 'ganwen-blog-database', 'version': 1,
            'posts': [{'id': 1, 'version': 1, 'type': 'post', 'status': 'publish',
                       'title': 'isolated fixture', 'content_html': '<p>fixture</p>',
                       'published_at': '2026-10-09T00:00:00Z',
                       'canonical_url': 'https://example.test/?p=1', 'translations': {}}],
            'values': [{'key': 'site', 'value': json.dumps({'title': 'fixture',
                        'base_url': 'https://example.test', 'posts_per_page': 10})},
                       {'key': 'private_provider_config', 'value': 'fixture configuration'}],
            'translation_jobs': [],
        }
        seed = self.base / 'seed.json'
        seed.write_text(json.dumps(document), encoding='utf-8')
        self.call_cli('database-import', self.data, seed)
        self.export = self.base / 'export.json'
        self.call_cli('database-export', self.data, self.export)
        self.document = json.loads(self.export.read_text(encoding='utf-8'))

    def call_cli(self, command, data, file):
        return subprocess.run([str(self.cli), command, '--database', 'sqlite',
                               '--data', str(data), '--file', str(file)],
                              env=self.environment, check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def real_cli_runner(self, argv, logpath):
        result = subprocess.run([str(self.cli), *argv[1:]], env=self.environment,
                                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        Path(logpath).write_text(result.stdout.decode('utf-8'), encoding='utf-8')

    def test_project_backup_real_cli_export_restore(self):
        self.assertNotIn('posts_revision_v1', {row['key'] for row in self.document['values']})
        stage = self.base / 'stage'
        stage.mkdir()
        result = backup.export_ganwen(self.root, stage, runner=self.real_cli_runner)
        self.assertEqual((result['posts'], result['kv'], result['translation_jobs']), (1, 2, 0))

    def test_restore_verifier_ignores_only_revision_from_legacy_backup(self):
        document = {**self.document, 'values': self.document['values'] +
                    [{'key': 'posts_revision_v1', 'value': 'old-cache-token'}]}
        file = self.base / 'legacy.json'
        file.write_text(json.dumps(document), encoding='utf-8')
        target = self.base / 'restored'
        self.call_cli('database-import', target, file)
        with closing(sqlite3.connect(target / 'blog.db')) as connection:
            token = connection.execute("SELECT value FROM kv WHERE key='posts_revision_v1'").fetchone()[0]
        self.assertNotEqual(token, 'old-cache-token')
        self.assertEqual(backup.verify_ganwen_restore(target, document)['kv'], 2)
        for mutation in (
                "UPDATE kv SET value='changed' WHERE key='private_provider_config'",
                "DELETE FROM kv WHERE key='site'",
                "INSERT INTO kv VALUES('admin_session_v1:fixture','unexpected session')"):
            with self.subTest(mutation=mutation):
                with closing(sqlite3.connect(target / 'blog.db')) as connection, connection:
                    connection.execute(mutation)
                with self.assertRaisesRegex(backup.BackupError, 'settings differ'):
                    backup.verify_ganwen_restore(target, document)
                with closing(sqlite3.connect(target / 'blog.db')) as connection, connection:
                    connection.execute("DELETE FROM kv WHERE key<>'posts_revision_v1'")
                    connection.executemany('INSERT INTO kv VALUES(?,?)',
                        [(row['key'], row['value']) for row in self.document['values']])

    def prepare_package(self):
        for folder in ('data/media', 'migration', 'deploy', 'dist', 'scripts'):
            (self.root / folder).mkdir(parents=True, exist_ok=True)
        (self.data / 'runtime.env').write_text('GBLOG_ADMIN_USER=isolated-fixture\n', encoding='utf-8')
        (self.data / 'admin-access.txt').write_text('isolated fixture\n', encoding='utf-8')
        (self.root / 'migration/wordpress.json').write_text('{}\n', encoding='utf-8')
        (self.root / 'migration/media-manifest.json').write_text('[]\n', encoding='utf-8')
        (self.root / 'scripts/media-aliases.json').write_text('[]\n', encoding='utf-8')
        (self.root / 'README.md').write_text('isolated fixture\n', encoding='utf-8')
        for name in ('BT-GO-PROJECT.md', 'ganwen-go.bt.conf.example', 'ganwen-blog.service',
                     'ganwen.nl.bt.conf.example', 'BT-DEPLOY.md', 'geoip-LICENSE-itbdw.txt'):
            (self.root / 'deploy' / name).write_text('isolated fixture\n', encoding='utf-8')
        # The package checks this file's header; it never executes this Linux fixture.
        header = bytearray(20)
        header[:6] = b'\x7fELF\x02\x01'
        header[18:20] = (62).to_bytes(2, 'little')
        (self.root / 'linux-fixture').write_bytes(header)
        path = self.root / 'scripts/package-bt.py'
        shutil.copy2(ROOT / 'scripts/package-bt.py', path)
        return load_script('restore_package_under_test', path)

    def run_package(self, package, name, document):
        file = self.base / (name + '.json')
        file.write_text(json.dumps(document), encoding='utf-8')
        output = self.root / 'dist' / (name + '.tar.gz')
        argv = [str(package.__file__), '--backup', str(file), '--output', str(output),
                '--cli', str(self.cli), '--binary', str(self.root / 'linux-fixture')]
        previous = Path.cwd()
        try:
            with mock.patch.object(sys, 'argv', argv), redirect_stdout(io.StringIO()) as captured:
                package.main()
            return json.loads(captured.getvalue())
        finally:
            os.chdir(previous)

    def test_package_real_cli_restore_with_and_without_legacy_revision(self):
        package = self.prepare_package()
        for name, values in (('normal', self.document['values']),
                ('legacy', self.document['values'] + [{'key': 'posts_revision_v1', 'value': 'old-cache-token'}])):
            with self.subTest(name=name):
                result = self.run_package(package, name, {**self.document, 'values': values})
                self.assertTrue(Path(result['package']).is_file())
                self.assertEqual(result['records'], 1)

    def test_package_still_rejects_configuration_or_session_changes(self):
        package = self.prepare_package()
        real_run = subprocess.run
        for name, mutation in (
                ('configuration', "UPDATE kv SET value='changed' WHERE key='private_provider_config'"),
                ('session', "INSERT INTO kv VALUES('admin_session_v1:fixture','unexpected session')")):
            def import_then_mutate(argv, **kwargs):
                result = real_run(argv, **kwargs)
                restored = Path(argv[argv.index('--data') + 1])
                with closing(sqlite3.connect(restored / 'blog.db')) as connection, connection:
                    connection.execute(mutation)
                return result
            with self.subTest(name=name), mock.patch.object(package.subprocess, 'run', import_then_mutate):
                with self.assertRaisesRegex(ValueError, 'settings differ'):
                    self.run_package(package, name, self.document)


if __name__ == '__main__':
    unittest.main()
