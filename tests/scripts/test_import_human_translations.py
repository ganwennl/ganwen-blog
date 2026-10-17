"""Isolated regression checks for the importer/cache revision contract."""
import importlib.util
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('import_human', (Path(__file__).resolve().parents[2] / 'scripts/migration/import-human-translations.py'))
importer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(importer)


class RevisionTests(unittest.TestCase):
    def test_unpublished_import_is_opt_in_and_never_publishes_original(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            database = root / 'blog.db'
            posts = [{'id': index, 'version': 1, 'type': 'post', 'status': status,
                      'title': '原文', 'content_html': '<p>正文</p>', 'excerpt_html': '', 'translations': {}}
                     for index, status in enumerate(('draft', 'private', 'pending'), 1)]
            with closing(sqlite3.connect(database)) as connection, connection:
                connection.executescript('CREATE TABLE posts(id INTEGER PRIMARY KEY,document TEXT); CREATE TABLE kv(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY,status TEXT);')
                for post in posts:
                    connection.execute('INSERT INTO posts VALUES(?,?)', (post['id'], json.dumps(post, ensure_ascii=False)))
                connection.execute('INSERT INTO kv VALUES(?,?)', ('translation_config_v1', '{"execution_mode":"manual"}'))
            translations = {post['id']: {'en': {'title': 'Original', 'content_html': '<p>Body</p>', 'excerpt_html': '', 'seo': {'title': '', 'description': ''}}} for post in posts}
            with self.assertRaisesRegex(AssertionError, 'eligible'):
                importer.import_translations(database, posts, translations, None, False, 'manual')
            importer.import_translations(database, posts, translations, root / 'backup', True, 'manual', True)
            with closing(sqlite3.connect(database)) as connection:
                restored = [json.loads(row[0]) for row in connection.execute('SELECT document FROM posts ORDER BY id')]
            for before, after in zip(posts, restored):
                self.assertEqual(after['status'], before['status'])
                self.assertEqual(after['translations']['en']['status'], 'review')
                self.assertEqual({k: v for k, v in after.items() if k not in ('version', 'translations')},
                                 {k: v for k, v in before.items() if k not in ('version', 'translations')})

    def test_source_quoted_phrase_allowlist_remains_narrow(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            post = {'id': 1, 'type': 'post', 'status': 'publish', 'title': '原文',
                    'content_html': '<p>参与翻墙破网行动。</p>', 'excerpt_html': ''}
            item = {'id': 1, 'title': 'Original', 'content_html': '<p>The campaign “翻墙破网”, bypassing censorship.</p>',
                    'excerpt_html': '', 'seo': {'title': '', 'description': ''}}
            file = root / 'en.json'
            file.write_text(json.dumps([item], ensure_ascii=False), encoding='utf-8')
            with self.assertRaisesRegex(AssertionError, 'untranslated Chinese'):
                importer.validate([post], root, ('en',))
            self.assertIn(1, importer.validate([post], root, ('en',), allowed_source_phrases={'1': ['翻墙破网']}))
            with self.assertRaisesRegex(AssertionError, 'must occur in the source'):
                importer.validate([post], root, ('en',), allowed_source_phrases={'1': ['未出现的名称']})
            item['content_html'] = item['content_html'].replace('</p>', '没翻译</p>')
            file.write_text(json.dumps([item], ensure_ascii=False), encoding='utf-8')
            with self.assertRaisesRegex(AssertionError, 'untranslated Chinese'):
                importer.validate([post], root, ('en',), allowed_source_phrases={'1': ['翻墙破网']})

    def test_empty_original_draft_is_not_filled_with_invented_content(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            post = {'id': 1, 'type': 'post', 'status': 'draft', 'title': '原文', 'content_html': '', 'excerpt_html': ''}
            item = {'id': 1, 'title': 'Original', 'content_html': '', 'excerpt_html': '', 'seo': {'title': '', 'description': ''}}
            (root / 'en.json').write_text(json.dumps([item]), encoding='utf-8')
            with self.assertRaisesRegex(AssertionError, 'eligible'):
                importer.validate([post], root, ('en',))
            self.assertIn(1, importer.validate([post], root, ('en',), include_unpublished=True))

    def test_padded_revision_collision_rolls_back_entire_import(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            database = root / 'blog.db'
            post = {'id': 1, 'version': 1, 'type': 'post', 'status': 'publish',
                    'title': '原文', 'content_html': '<p>正文</p>', 'excerpt_html': '', 'translations': {}}
            config = {'execution_mode': 'manual', 'endpoint': 'fixture'}
            with closing(sqlite3.connect(database)) as connection, connection:
                connection.executescript('CREATE TABLE posts(id INTEGER PRIMARY KEY,document TEXT); CREATE TABLE kv(key TEXT COLLATE RTRIM PRIMARY KEY,value TEXT); CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY,status TEXT);')
                connection.execute('INSERT INTO posts VALUES(?,?)', (1, json.dumps(post, ensure_ascii=False)))
                connection.execute('INSERT INTO kv VALUES(?,?)', ('translation_config_v1', json.dumps(config)))
                connection.execute('INSERT INTO kv VALUES(?,?)', ('posts_revision_v1 ', 'preserve-padded-key'))
                self.assertEqual(connection.execute(
                    "SELECT COUNT(*) FROM kv WHERE key COLLATE BINARY='posts_revision_v1'").fetchone()[0], 0)
            translations = {1: {'en': {'title': 'Original', 'content_html': '<p>Body</p>', 'excerpt_html': '', 'seo': {'title': '', 'description': ''}}}}

            def current():
                with closing(sqlite3.connect(database)) as connection:
                    return (connection.execute('SELECT document FROM posts WHERE id=1').fetchone()[0],
                            dict(connection.execute('SELECT key,value FROM kv')))

            original = current()
            with self.assertRaisesRegex(ValueError, 'revision key collides'):
                importer.import_translations(database, [post], translations, root / 'backup', True, 'paused')
            self.assertEqual(current(), original, 'failed import changed the article, execution mode or revision alias')
            with closing(sqlite3.connect(database)) as connection:
                self.assertEqual(connection.execute(
                    "SELECT COUNT(*) FROM kv WHERE key COLLATE BINARY='posts_revision_v1'").fetchone()[0], 0)

    def test_apply_changes_revision_and_dry_run_rolls_back(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            database = root / 'blog.db'
            post = {'id': 1, 'version': 1, 'type': 'post', 'status': 'publish',
                    'title': '原文', 'content_html': '<p>正文</p>', 'excerpt_html': '', 'translations': {}}
            with closing(sqlite3.connect(database)) as connection, connection:
                connection.executescript('CREATE TABLE posts(id INTEGER PRIMARY KEY,document TEXT); CREATE TABLE kv(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY,status TEXT);')
                connection.execute('INSERT INTO posts VALUES(?,?)', (1, json.dumps(post, ensure_ascii=False)))
                connection.execute('INSERT INTO kv VALUES(?,?)', ('translation_config_v1', json.dumps({'execution_mode': 'manual', 'encrypted_api_key': 'fixture'})))
                connection.execute('INSERT INTO kv VALUES(?,?)', ('posts_revision_v1', 'old-token'))
            translations = {1: {'en': {'title': 'Original', 'content_html': '<p>Body</p>', 'excerpt_html': '', 'seo': {'title': '', 'description': ''}}}}

            def current():
                with closing(sqlite3.connect(database)) as connection:
                    return (connection.execute('SELECT document FROM posts WHERE id=1').fetchone()[0],
                            dict(connection.execute('SELECT key,value FROM kv')))

            original = current()
            importer.import_translations(database, [post], translations, None, False, 'manual')
            self.assertEqual(current(), original)
            result = importer.import_translations(database, [post], translations, root / 'backup', True, 'manual')
            raw, values = current()
            updated = json.loads(raw)
            self.assertTrue(result['applied'])
            self.assertEqual(updated['version'], 2)
            self.assertEqual(updated['translations']['en']['source_hash'], importer.source_hash(post))
            self.assertNotEqual(values['posts_revision_v1'], 'old-token')
            self.assertEqual(len(values['posts_revision_v1']), 32)
            self.assertEqual(json.loads(values['translation_config_v1']), json.loads(original[1]['translation_config_v1']))
            self.assertTrue((root / 'backup' / 'before.sqlite').is_file())
            before_failure = current()
            with self.assertRaisesRegex(AssertionError, 'post changed'):
                importer.import_translations(database, [post], translations, None, False, 'manual')
            self.assertEqual(current(), before_failure)


if __name__ == '__main__':
    unittest.main()
