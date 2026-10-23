"""Offline publication-state regression tests; no live website or credentials."""
import importlib.util
import json
from email.message import Message
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
import urllib.parse
import xml.etree.ElementTree as ET


spec = importlib.util.spec_from_file_location('verify_production', (Path(__file__).resolve().parents[2] / 'scripts/verify/verify-production.py'))
verify = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = verify
spec.loader.exec_module(verify)
BASE = 'https://www.ganwen.nl'


def article(identifier, kind='post', tags=None):
    query = 'page_id' if kind == 'page' else 'p'
    return {'id': identifier, 'type': kind, 'status': 'publish',
            'canonical_url': f'{BASE}/?{query}={identifier}', 'tags': tags or []}


class PublicationTests(unittest.TestCase):
    def test_explicit_review_languages_are_absent_and_legacy_ids_stay_strict(self):
        page = article(2, 'page')
        tasks, absent = verify.publication_plan(page, {'2': frozenset({'zh'})})
        self.assertEqual(len(tasks), 1)
        self.assertEqual(tasks[0][2], {'zh-Hans': BASE + '/?page_id=2', 'x-default': BASE + '/?page_id=2'})
        self.assertEqual(len(absent), 10)
        self.assertIn(BASE + '/en/?page_id=2', absent)
        legacy, absent = verify.publication_plan(article(3), {'2': frozenset({'zh'})})
        self.assertEqual(len(legacy), 11)
        self.assertEqual(len(legacy[0][2]), 12)
        self.assertEqual(absent, set())

    def test_manifest_rejects_unsupported_or_ambiguous_language_contracts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'publication.json'
            path.write_text(json.dumps({'2': ['zh', 'en']}), encoding='utf-8')
            self.assertEqual(verify.publication_manifest(path), {'2': frozenset({'zh', 'en'})})
            for invalid in [[], {'02': ['zh']}, {'2': ['en']}, {'2': ['zh', 'xx']},
                            {'2': ['zh', 'zh']}, {'2': 'zh'}, {'2': ['zh', {}]}]:
                with self.subTest(invalid=invalid):
                    path.write_text(json.dumps(invalid), encoding='utf-8')
                    with self.assertRaises(verify.CheckFailed):
                        verify.publication_manifest(path)

    def test_explicit_predeployment_publication_overrides_old_draft_only(self):
        published = article(1)
        later_published = dict(article(27), status='draft')
        private = dict(article(100), status='private')
        source = {'posts': [published, later_published, private]}
        current, protected = verify.publication_articles(source, {'27': {'zh', 'en'}})
        self.assertEqual([post['id'] for post in current], [1, 27])
        self.assertEqual([post['id'] for post in protected], [100])
        legacy_current, legacy_protected = verify.publication_articles(source, {})
        self.assertEqual([post['id'] for post in legacy_current], [1])
        self.assertEqual([post['id'] for post in legacy_protected], [27, 100])

    def test_tags_use_current_post_languages_but_empty_categories_remain(self):
        tag = lambda slug: {'slug': slug, 'url': BASE + '/?tag=' + slug}
        source = {'settings': {'categories': [{'id': 13, 'url': BASE + '/?cat=13'}]}}
        posts = [article(1, tags=[tag('shared'), tag('limited')]),
                 article(2, tags=[tag('shared')]), article(3, 'page', [tag('page-only')])]
        tasks = verify.taxonomy_tasks(source, posts, {'1': {'zh'}, '2': {'zh', 'en'}})
        self.assertEqual(len(tasks), 14)  # 11 category routes + 2 shared tags + 1 limited tag.
        self.assertIn(('/ur/?cat=13', 'ur'), tasks)
        self.assertIn(('/en/?tag=shared', 'en'), tasks)
        self.assertNotIn(('/en/?tag=limited', 'en'), tasks)
        self.assertFalse(any('page-only' in path for path, _ in tasks))

    def test_new_sitemap_publications_also_check_unpublished_languages(self):
        expected, absent = verify.extra_publication_plan(
            {BASE + '/?p=10001'}, {'10001': frozenset({'zh'})})
        self.assertEqual(expected, {BASE + '/?p=10001'})
        self.assertEqual(len(absent), 10)
        self.assertIn(BASE + '/en/?p=10001', absent)
        legacy_expected, legacy_absent = verify.extra_publication_plan({BASE + '/?p=10002'}, {})
        self.assertEqual((legacy_expected, legacy_absent), (set(), set()))

    def test_page_sitemap_home_is_not_a_new_article(self):
        client = SimpleNamespace(base=urllib.parse.urlsplit(BASE), port=443)
        result = verify.sitemap_extra_urls(client, {BASE + '/', BASE + '/?p=10001'},
                                           set(), set(), set(), '/?sitemap=index')
        self.assertEqual(result, {BASE + '/?p=10001'})
        with self.assertRaises(verify.CheckFailed):
            verify.sitemap_extra_urls(client, {BASE + '/unexpected/'}, set(), set(), set(), '/?sitemap=index')

    def run_fixture(self, available_translation=False, sitemap_translation=False):
        requested = []

        class Client:
            base = urllib.parse.urlsplit(BASE)
            port = 443
            origin_host = None

            def fetch(self, path, method='GET', headers=None):
                requested.append(path)
                response_headers = Message()
                if path.startswith('/admin/'):
                    response_headers['Cache-Control'] = 'no-store'
                    response_headers['X-Robots-Tag'] = 'noindex'
                    if path == '/admin/api/session':
                        return verify.Response(200, response_headers, b'{"authenticated":false,"csrf":"test","configured":true}')
                    if path == '/admin/':
                        return verify.Response(200, response_headers, b'')
                    return verify.Response(401, response_headers, b'{"error":"login required"}')
                if 'feed=' in path:
                    response_headers['X-Robots-Tag'] = 'noindex'
                    return verify.Response(410, response_headers, b'')
                if path == '/robots.txt':
                    return verify.Response(200, response_headers,
                                           ('Disallow: /admin\nSitemap: ' + BASE + '/?sitemap=index\n').encode())
                if path == '/en/?page_id=2' and available_translation:
                    return verify.Response(200, response_headers, b'wrongly exposed review translation')
                return verify.Response(404, response_headers, b'')

        def sitemap(_client, path, _root):
            namespace = 'http://www.sitemaps.org/schemas/sitemap/0.9'
            if path == '/?sitemap=index':
                return ET.fromstring(f'<sitemapindex xmlns="{namespace}"><sitemap><loc>{BASE}/?sitemap=posts-post-1</loc></sitemap><sitemap><loc>{BASE}/?sitemap=posts-page-1</loc></sitemap></sitemapindex>')
            locations = [BASE + '/?page_id=2'] if path.endswith('posts-page-1') else []
            if sitemap_translation:
                locations.append(BASE + '/en/?page_id=2')
            return ET.fromstring(f'<urlset xmlns="{namespace}">' + ''.join(f'<url><loc>{url}</loc></url>' for url in locations) + '</urlset>')

        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'source.json'
            manifest = Path(directory) / 'publication.json'
            source.write_text(json.dumps({'posts': [article(2, 'page')]}), encoding='utf-8')
            manifest.write_text(json.dumps({'2': ['zh']}), encoding='utf-8')
            args = SimpleNamespace(base_url=BASE, origin_host=None, timeout=2, workers=2,
                                   source=source, publication_manifest=manifest, media_manifest=None)
            with mock.patch.object(verify, 'Client', return_value=Client()), \
                    mock.patch.object(verify, 'public_document', return_value=set()) as document, \
                    mock.patch.object(verify, 'xml_response', side_effect=sitemap), \
                    mock.patch.object(verify, 'language_checks', return_value=0), \
                    mock.patch.object(verify, 'taxonomy_checks', return_value=(0, set())), \
                    mock.patch.object(verify, 'blocked_path_check', side_effect=lambda _client, path: (path, 404)):
                result = verify.verify(args)
                self.assertEqual(document.call_count, 2)  # Chinese canonical + legacy /index.php.
                for call in document.call_args_list:
                    self.assertEqual(set(call.args[3]), {'zh-Hans', 'x-default'})
                self.assertIn('/en/?page_id=2', requested)
                return result

    def test_verifier_requires_404_for_each_unpublished_language(self):
        result = self.run_fixture()
        self.assertEqual(result['public_language_pages'], 1)
        self.assertEqual(result['unpublished_translation_404s'], 10)
        with self.assertRaisesRegex(verify.CheckFailed, 'expected 404, got 200'):
            self.run_fixture(available_translation=True)

    def test_verifier_rejects_unpublished_translation_in_sitemap(self):
        with self.assertRaisesRegex(verify.CheckFailed, 'unpublished or outdated translations exposed'):
            self.run_fixture(sitemap_translation=True)


class ContactNoticeTests(unittest.TestCase):
    contacts = (('x', 'https://x.com/ganwen_nl'), ('email', 'mailto:ganwen94@gmail.com'))
    dialog = '<dialog data-contact-notice><p>请先说明来意。</p></dialog>'

    def check(self, contacts, notice=''):
        response_headers = Message()
        response_headers['Content-Type'] = 'text/html; charset=utf-8'
        body = ('<html lang="zh-Hans"><head><link rel="canonical" href="' + BASE + '/">'
                '<link rel="alternate" hreflang="zh-Hans" href="' + BASE + '/">'
                '</head><body>' + contacts + notice + '</body></html>')
        response = verify.Response(200, response_headers, body.encode('utf-8'))
        client = SimpleNamespace(fetch=lambda _path: response)
        return verify.public_document(client, '/', BASE + '/', {'zh-Hans': BASE + '/'}, 'zh-Hans')

    def buttons(self):
        return ''.join(f'<button type="button" data-contact-kind="{kind}" '
                       f'data-contact-url="{url}">联系</button>' for kind, url in self.contacts)

    def links(self):
        return ''.join(f'<a data-contact-kind="{kind}" href="{url}">联系</a>'
                       for kind, url in self.contacts)

    def test_disabled_notice_preserves_direct_contact_verification(self):
        self.assertEqual(self.check(self.links()), set())

    def test_enabled_notice_verifies_button_destinations_and_dialog(self):
        self.assertEqual(self.check(self.buttons(), self.dialog), set())
        parsed = verify.Document((self.buttons() + self.dialog).encode('utf-8'), '/')
        self.assertEqual(parsed.contacts, {kind: {url} for kind, url in self.contacts})
        self.assertEqual(parsed.contact_notice_dialogs, 1)
        self.assertEqual(parsed.contact_buttons, 2)
        self.assertEqual(parsed.contact_links, 0)

    def test_enabled_notice_requires_exactly_one_real_dialog(self):
        for notice in ('', '<div data-contact-notice>提示</div>', '<dialog>提示</dialog>',
                       self.dialog + self.dialog):
            with self.subTest(notice=notice):
                with self.assertRaisesRegex(verify.CheckFailed, 'exactly one notice dialog'):
                    self.check(self.buttons(), notice)

    def test_enabled_notice_rejects_mixed_direct_contact_links(self):
        with self.assertRaisesRegex(verify.CheckFailed, 'direct contact link bypasses'):
            self.check(self.buttons() + self.links(), self.dialog)

    def test_enabled_notice_requires_correct_destinations_and_button_type(self):
        with self.assertRaisesRegex(verify.CheckFailed, 'original X contact missing'):
            self.check(self.buttons().replace('https://x.com/ganwen_nl', 'https://x.com/other'), self.dialog)
        with self.assertRaisesRegex(verify.CheckFailed, 'original email contact missing'):
            self.check(self.buttons().replace('data-contact-url="mailto:', 'data-wrong="mailto:'), self.dialog)
        for invalid in (self.buttons().replace('type="button"', 'type="submit"'),
                        self.buttons().replace('<button ', '<button href="https://example.com/" ')):
            with self.subTest(invalid=invalid):
                with self.assertRaisesRegex(verify.CheckFailed, 'invalid contact consent button'):
                    self.check(invalid, self.dialog)


if __name__ == '__main__':
    unittest.main()
