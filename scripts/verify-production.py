"""Read-only HTTPS checks for the deployed Ganwen blog, without credentials."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from html.parser import HTMLParser
import http.client
import ipaddress
import json
from pathlib import Path
import re
import socket
import ssl
import sys
import urllib.parse
import xml.etree.ElementTree as ET


LANGUAGES = tuple((code, '' if code == 'zh' else '/' + code,
                   'zh-Hans' if code == 'zh' else code)
                  for code in ('zh', 'nl', 'en', 'hi', 'es', 'ar', 'fr', 'bn', 'pt', 'id', 'ur'))
LANGUAGE_CODES = frozenset(code for code, _, _ in LANGUAGES)
MAX_BODY = 16 * 1024 * 1024


class CheckFailed(Exception):
    """A diagnostic containing check names and paths, never response contents."""


def require(condition, path, check):
    if not condition:
        raise CheckFailed(f'{path}: {check}')


class OriginHTTPSConnection(http.client.HTTPSConnection):
    """Override only the TCP destination; TLS identity remains the URL host."""

    def __init__(self, host, port, origin_host, context, timeout):
        super().__init__(host, port, context=context, timeout=timeout)
        self.origin_host = origin_host

    def connect(self):
        # No tunnel/proxy or insecure context is accepted by this verifier.
        if self._tunnel_host:
            raise ValueError('Proxy tunnels are not supported')
        if not self._context.check_hostname or self._context.verify_mode != ssl.CERT_REQUIRED:
            raise ValueError('TLS certificate verification must remain enabled')
        sock = socket.create_connection((self.origin_host, self.port), self.timeout,
                                        self.source_address)
        try:
            self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        except BaseException:
            sock.close()
            raise


@dataclass
class Response:
    status: int
    headers: http.client.HTTPMessage
    body: bytes


class Client:
    def __init__(self, base_url, origin_host, timeout):
        self.base = urllib.parse.urlsplit(base_url)
        if (self.base.scheme != 'https' or not self.base.hostname
                or self.base.username is not None or self.base.password is not None
                or self.base.path not in ('', '/') or self.base.query or self.base.fragment):
            raise ValueError('base-url must be an HTTPS origin without credentials or a path')
        self.port = self.base.port or 443
        self.origin_host = origin_host
        if origin_host:
            ipaddress.ip_address(origin_host)
        self.timeout = timeout
        self.context = ssl.create_default_context()
        self.context.set_alpn_protocols(['http/1.1'])

    def fetch(self, path, method='GET', headers=None):
        require(method in ('GET', 'HEAD'), path, 'verification only permits GET and HEAD')
        require(path.startswith('/') and not path.startswith('//')
                and not any(c in path for c in '\r\n'), path, 'invalid request path')
        if self.origin_host:
            connection = OriginHTTPSConnection(self.base.hostname, self.port,
                                               self.origin_host, self.context, self.timeout)
        else:
            connection = http.client.HTTPSConnection(self.base.hostname, self.port,
                                                     context=self.context, timeout=self.timeout)
        request_headers = {'User-Agent': 'Mozilla/5.0 (compatible; GanwenMigrationVerifier/1.0)',
                           'Accept-Encoding': 'identity', 'Connection': 'close'}
        request_headers.update(headers or {})
        try:
            connection.request(method, path, headers=request_headers)
            response = connection.getresponse()
            body = response.read(0 if method == 'HEAD' else MAX_BODY + 1)
            require(len(body) <= MAX_BODY, path, 'response exceeds verification limit')
            return Response(response.status, response.headers, body)
        except CheckFailed:
            raise
        except ssl.SSLCertVerificationError as error:
            raise CheckFailed(f'{path}: TLS certificate validation failed') from error
        except (OSError, http.client.HTTPException) as error:
            raise CheckFailed(f'{path}: HTTPS request failed ({type(error).__name__})') from error
        finally:
            connection.close()


def email_contact_href(value):
    """Cloudflare rewrites mailto links; verify the decoded address exactly."""
    parsed = urllib.parse.urlsplit(value)
    if (parsed.path != '/cdn-cgi/l/email-protection'
            or parsed.scheme not in ('', 'https')
            or (parsed.netloc and parsed.hostname not in ('ganwen.nl', 'www.ganwen.nl'))
            or parsed.username or parsed.password or parsed.query):
        return value
    encoded = parsed.fragment
    if len(encoded) < 4 or len(encoded) > 2048 or len(encoded) % 2 or not re.fullmatch('[0-9a-fA-F]+', encoded):
        return value
    try:
        raw = bytes.fromhex(encoded)
        address = bytes(byte ^ raw[0] for byte in raw[1:]).decode('utf-8')
    except (ValueError, UnicodeError):
        return value
    return 'mailto:' + address


class Document(HTMLParser):
    def __init__(self, body, path):
        super().__init__(convert_charrefs=True)
        self.lang = ''
        self.canonical = []
        self.hreflang = {}
        self.robots = []
        self.contacts = {}
        self.current_taxonomies = []
        self.resources = set()
        try:
            self.feed(body.decode('utf-8'))
            self.close()
        except (UnicodeError, ValueError) as error:
            raise CheckFailed(f'{path}: invalid UTF-8 HTML') from error

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == 'html':
            self.lang = values.get('lang', '')
        if tag == 'meta' and values.get('name', '').lower() in ('robots', 'googlebot', 'bingbot'):
            self.robots.append(values.get('content', ''))
        if tag == 'link':
            relations = values.get('rel', '').lower().split()
            if 'canonical' in relations:
                self.canonical.append(values.get('href', ''))
            if values.get('hreflang'):
                key = values['hreflang']
                # Duplicate language annotations are an invalid SEO signal.
                self.hreflang.setdefault(key, []).append(values.get('href', ''))
            if 'stylesheet' in relations and values.get('href'):
                self.resources.add(values['href'])
        if tag == 'a' and values.get('data-contact-kind'):
            kind = values['data-contact-kind']
            value = values.get('href', '')
            self.contacts.setdefault(kind, set()).add(
                email_contact_href(value) if kind == 'email' else value)
        if tag == 'a' and values.get('aria-current') == 'page':
            self.current_taxonomies.append(values.get('href', ''))
        if tag in ('img', 'video', 'audio', 'source', 'script'):
            for key in ('src', 'poster'):
                if values.get(key):
                    self.resources.add(values[key])
            for item in values.get('srcset', '').split(','):
                if item.strip():
                    self.resources.add(item.strip().split()[0])


def noindex(values):
    return any(re.search(r'(?<![a-z])(?:noindex|none)(?![a-z])', value.lower())
               for value in values)


def publicly_indexable(response, path, document=None):
    values = response.headers.get_all('X-Robots-Tag', [])
    if document:
        values += document.robots
    require(not noindex(values), path, 'public response forbids indexing')


def anonymous_only(response, path):
    require(noindex(response.headers.get_all('X-Robots-Tag', [])), path,
            'admin response lacks noindex')
    require('no-store' in response.headers.get('Cache-Control', '').lower(), path,
            'admin response is cacheable')


def route_for(url):
    parsed = urllib.parse.urlsplit(url)
    return urllib.parse.urlunsplit(('', '', parsed.path or '/', parsed.query, ''))


def translated_url(original, prefix):
    parsed = urllib.parse.urlsplit(original)
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc,
                                  prefix + (parsed.path or '/'), parsed.query, ''))


def public_document(client, path, canonical, expected_links, html_lang):
    response = client.fetch(path)
    require(response.status == 200, path, f'expected 200, got {response.status}')
    require('text/html' in response.headers.get('Content-Type', '').lower(), path,
            'expected HTML response')
    document = Document(response.body, path)
    require(document.lang == html_lang, path, 'incorrect HTML language')
    require(document.canonical == [canonical], path, 'canonical mismatch')
    require(document.hreflang == {key: [value] for key, value in expected_links.items()},
            path, 'hreflang mismatch or duplicate')
    publicly_indexable(response, path, document)
    require('https://x.com/ganwen_nl' in document.contacts.get('x', set()), path,
            'original X contact missing')
    require('mailto:ganwen94@gmail.com' in document.contacts.get('email', set()), path,
            'original email contact missing')
    return document.resources


def require_status(client, path, status):
    response = client.fetch(path)
    require(response.status == status, path, f'expected {status}, got {response.status}')
    return response


def publication_manifest(manifest_file):
    """An explicit deployment snapshot narrows languages; absent IDs stay strict."""
    if not manifest_file:
        return {}
    manifest = json.loads(Path(manifest_file).read_text(encoding='utf-8'))
    require(isinstance(manifest, dict), '--publication-manifest', 'expected an article ID/languages mapping')
    result = {}
    for identifier, languages in manifest.items():
        require(isinstance(identifier, str) and re.fullmatch(r'[1-9][0-9]*', identifier),
                '--publication-manifest', 'invalid article ID')
        require(isinstance(languages, list)
                and all(isinstance(code, str) and code in LANGUAGE_CODES for code in languages)
                and len(set(languages)) == len(languages) and 'zh' in languages,
                '--publication-manifest', 'languages must be unique supported codes including zh')
        result[identifier] = frozenset(languages)
    return result


def published_language_codes(post, manifest):
    return manifest.get(str(post['id']), LANGUAGE_CODES)


def publication_articles(source, manifest):
    articles = [post for post in source['posts'] if post['type'] in ('post', 'page')]
    # A trusted predeployment manifest also records articles published since
    # migration. Without that explicit evidence, private/draft checks stay strict.
    public = [post for post in articles if post['status'] == 'publish' or str(post['id']) in manifest]
    private = [post for post in articles if post['status'] in ('draft', 'private') and str(post['id']) not in manifest]
    return public, private


def publication_links(post, manifest):
    codes = published_language_codes(post, manifest)
    links = {html_lang: translated_url(post['canonical_url'], prefix)
             for code, prefix, html_lang in LANGUAGES if code in codes}
    links['x-default'] = post['canonical_url']
    return links


def publication_plan(post, manifest):
    codes = published_language_codes(post, manifest)
    links = publication_links(post, manifest)
    tasks, unavailable = [], set()
    for code, prefix, html_lang in LANGUAGES:
        canonical = translated_url(post['canonical_url'], prefix)
        if code in codes:
            tasks.append((route_for(canonical), canonical, links, html_lang))
        else:
            unavailable.add(canonical)
    return tasks, unavailable


def taxonomy_tasks(source, public_posts, manifest):
    terms = {('category', str(term['id'])): term
             for term in source.get('settings', {}).get('categories', [])}
    tag_languages = {}
    for post in public_posts:
        # Standalone pages do not contribute to the blog's tag navigation.
        if post['type'] != 'post':
            continue
        for term in post.get('tags', []):
            key = ('tag', urllib.parse.unquote(term['slug']))
            terms[key] = term
            tag_languages.setdefault(key, set()).update(published_language_codes(post, manifest))
    tasks = []
    for key, term in terms.items():
        original = urllib.parse.urlsplit(term['url'])
        for code, prefix, html_lang in LANGUAGES:
            # Empty configured categories remain public in every language.
            # Tags are only shown when a currently published translation uses them.
            if key[0] == 'tag' and code not in tag_languages[key]:
                continue
            tasks.append((prefix + '/' + ('?' + original.query if original.query else ''), html_lang))
    return tasks


def taxonomy_checks(client, source, public_posts, workers, manifest=None):
    tasks = taxonomy_tasks(source, public_posts, manifest or {})

    def check(task):
        path, html_lang = task
        response = require_status(client, path, 200)
        document = Document(response.body, path)
        require(document.lang == html_lang, path, 'taxonomy language mismatch')
        publicly_indexable(response, path, document)
        require(len(document.canonical) == 1, path, 'taxonomy canonical missing or duplicated')
        expected = urllib.parse.urlsplit(client.base.geturl().rstrip('/') + path)
        actual = urllib.parse.urlsplit(document.canonical[0])
        require(actual.scheme == expected.scheme and actual.netloc == expected.netloc
                and actual.path == expected.path
                and urllib.parse.parse_qs(actual.query) == urllib.parse.parse_qs(expected.query),
                path, 'original taxonomy canonical changed')
        expected_query = urllib.parse.parse_qs(expected.query)
        require(any(urllib.parse.parse_qs(urllib.parse.urlsplit(link).query) == expected_query
                    for link in document.current_taxonomies), path,
                'selected category/tag is not visible in navigation')
        return document.resources

    resources = set()
    with ThreadPoolExecutor(max_workers=workers) as pool:
        for found in pool.map(check, tasks):
            resources.update(found)
    return len(tasks), resources


def local_resource_path(value, client):
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme not in ('', 'http', 'https') or parsed.username or parsed.password:
        return None
    if parsed.netloc and parsed.hostname not in (client.base.hostname, 'ganwen.nl', 'www.ganwen.nl'):
        return None
    if parsed.netloc and parsed.port not in (None, 80, 443, client.port):
        return None
    path = urllib.parse.quote(urllib.parse.unquote(parsed.path), safe='/!$&\'()*+,;=:@-._~')
    if not path.startswith('/'):
        path = '/' + path
    return path + ('?' + parsed.query if parsed.query else '')


def original_media_paths(manifest_file):
    """Preserve original uploads even when the current HTML no longer links them."""
    if not manifest_file:
        return set()
    manifest = json.loads(Path(manifest_file).read_text(encoding='utf-8'))
    require(isinstance(manifest, list), '--media-manifest', 'expected a media record list')
    paths = set()
    for record in manifest:
        value = record.get('path') if isinstance(record, dict) else None
        require(isinstance(value, str) and bool(value) and len(value) <= 4096,
                '--media-manifest', 'invalid original media path')
        require(not any(ord(char) < 32 or ord(char) == 127 for char in value)
                and not any(char in value for char in ('\\', '?', '#')),
                '--media-manifest', 'invalid original media path characters')
        for candidate in (value, urllib.parse.unquote(value)):
            require((candidate.startswith('wp-content/uploads/')
                     or candidate in ('favicon.gif', 'favicon.ico', 'favicon.png'))
                    and all(part not in ('', '.', '..') for part in candidate.split('/'))
                    and '\\' not in candidate
                    and not any(ord(char) < 32 or ord(char) == 127 for char in candidate),
                    '--media-manifest', 'original media path escapes permitted media directories')
        paths.add('/' + urllib.parse.quote(value, safe='/!$&\'()*+,;=:@-._~'))
    return paths


def cloudflare_email_script(path):
    return bool(re.fullmatch(r'/cdn-cgi/scripts/[0-9a-fA-F]+/cloudflare-static/email-decode\.min\.js',
                             urllib.parse.urlsplit(path).path))


def cloudflare_edge_block(response):
    markers = (b'Attention Required! | Cloudflare', b'Sorry, you have been blocked',
               b'/cdn-cgi/styles/cf.errors.css', b'Cloudflare Ray ID')
    return (response.status == 403
            and response.headers.get('Server', '').lower() == 'cloudflare'
            and bool(response.headers.get('CF-Ray'))
            and all(marker in response.body for marker in markers))


def blocked_path_check(client, path):
    response = client.fetch(path)
    if response.status == 404:
        return path, 404
    # This exception describes edge denial only, never proof of an origin 404.
    # Direct-origin verification must always observe the actual 404 response.
    if not client.origin_host and cloudflare_edge_block(response):
        return path, 403
    raise CheckFailed(f'{path}: expected 404, got {response.status}')


def xml_response(client, path, root_name):
    response = require_status(client, path, 200)
    publicly_indexable(response, path)
    try:
        root = ET.fromstring(response.body)
    except ET.ParseError as error:
        raise CheckFailed(f'{path}: invalid XML response') from error
    require(root.tag.rsplit('}', 1)[-1] == root_name, path, 'incorrect XML root')
    return root


def sitemap_extra_urls(client, item_links, expected_urls, private_urls, private_ids, path):
    """Keep the migration privacy baseline while accepting later publications."""
    require(bool(item_links), path, 'sitemap has no article URLs')
    extras = set()
    for value in item_links:
        require(isinstance(value, str) and bool(value), path, 'sitemap has an empty article URL')
        parsed = urllib.parse.urlsplit(value)
        require(parsed.scheme == client.base.scheme and parsed.hostname == client.base.hostname
                and (parsed.port or 443) == client.port and not parsed.username
                and not parsed.password and not parsed.fragment,
                path, 'sitemap article URL does not match HTTPS base host')
        # The page sitemap intentionally includes the canonical Chinese home.
        if value == client.base.geturl().rstrip('/') + '/':
            continue
        query = urllib.parse.parse_qs(parsed.query)
        query_ids = set()
        for key in ('p', 'page_id'):
            for identifier in query.get(key, []):
                if identifier.isascii() and identifier.isdigit():
                    query_ids.add(str(int(identifier)))
        require(value not in private_urls and not private_ids.intersection(query_ids),
                route_for(value), 'migration private article URL exposed in sitemap')
        if value not in expected_urls:
            id_key = 'page_id' if 'page_id' in query else 'p'
            identifiers = query.get(id_key, [])
            require(parsed.path in tuple(prefix + '/' for _, prefix, _ in LANGUAGES)
                    and set(query) == {id_key} and len(identifiers) == 1
                    and re.fullmatch(r'[1-9][0-9]*', identifiers[0]) is not None,
                    path, 'new sitemap publication does not use a valid blog article route')
            extras.add(value)
    return extras


def sitemap_extra_document(client, value):
    path = route_for(value)
    response = require_status(client, path, 200)
    require('text/html' in response.headers.get('Content-Type', '').lower(), path,
            'new sitemap article did not return HTML')
    document = Document(response.body, path)
    publicly_indexable(response, path, document)
    require(document.canonical == [value], path,
            'new sitemap article canonical is not its own URL')
    return document.resources


def extra_publication_plan(extras, manifest):
    """Infer new article/page routes from sitemap entries, never from ID alone."""
    originals = {}
    for value in extras:
        parsed = urllib.parse.urlsplit(value)
        query = urllib.parse.parse_qs(parsed.query)
        id_key = 'page_id' if 'page_id' in query else 'p'
        identifier = query[id_key][0]
        if identifier not in manifest:
            continue
        original = urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, '/', parsed.query, ''))
        require(identifier not in originals or originals[identifier] == original,
                '/?sitemap=index', 'new publication has conflicting article/page routes')
        originals[identifier] = original
    expected, unavailable = set(), set()
    for identifier, original in originals.items():
        tasks, absent = publication_plan({'id': identifier, 'canonical_url': original}, manifest)
        expected.update(task[1] for task in tasks)
        unavailable.update(absent)
    return expected, unavailable


def language_checks(client, original_route):
    count = 0
    for language, _, _ in LANGUAGES[1:]:
        accept = language + ',zh;q=0.7'
        response = client.fetch('/', headers={'Accept-Language': accept})
        require(response.status == 302 and response.headers.get('Location') == f'/{language}/',
                '/', f'{language} browser language redirect mismatch')
        require('private' in response.headers.get('Cache-Control', '').lower()
                and 'no-store' in response.headers.get('Cache-Control', '').lower(),
                '/', 'language negotiation lacks private no-store')
        vary = {field.strip().lower() for value in response.headers.get_all('Vary', [])
                for field in value.split(',')}
        require({'accept-language', 'cookie'}.issubset(vary) or '*' in vary, '/',
                'language negotiation lacks Vary isolation')
        count += 1
    for headers in ({'Accept-Language': 'zh-CN,en;q=0.8'},
                    {'Accept-Language': 'es-ES', 'Cookie': 'ganwen_language=zh'},
                    {'Accept-Language': 'en-US', 'User-Agent': 'Googlebot'}):
        response = client.fetch('/', headers=headers)
        require(response.status == 200, '/', 'Chinese preference/crawler home not 200')
        document = Document(response.body, '/')
        require(document.lang == 'zh-Hans', '/', 'Chinese preference/crawler language mismatch')
        publicly_indexable(response, '/', document)
        count += 1
    response = client.fetch('/', headers={'Accept-Language': 'zh-CN',
                                         'Cookie': 'ganwen_language=nl'})
    require(response.status == 302 and response.headers.get('Location') == '/nl/', '/',
            'explicit language cookie does not override browser language')
    count += 1
    response = client.fetch(original_route, headers={'Accept-Language': 'es-ES',
                                                    'Cookie': 'ganwen_language=es'})
    require(response.status == 200, original_route, 'old article was language-redirected')
    document = Document(response.body, original_route)
    require(document.lang == 'zh-Hans', original_route, 'old article language changed')
    publicly_indexable(response, original_route, document)
    return count + 1


def verify(args):
    client = Client(args.base_url, args.origin_host, args.timeout)
    source = json.loads(Path(args.source).read_text(encoding='utf-8'))
    original_media = original_media_paths(args.media_manifest)
    manifest = publication_manifest(getattr(args, 'publication_manifest', None))
    public, private = publication_articles(source, manifest)
    require(bool(public), '--source', 'no published articles/pages in source')
    tasks, expected_urls, private_urls, unavailable_urls = [], set(), set(), set()
    for post in public:
        original = post['canonical_url']
        parsed = urllib.parse.urlsplit(original)
        require(parsed.scheme == 'https' and parsed.hostname == client.base.hostname
                and not parsed.username and not parsed.password and not parsed.fragment,
                f"post {post['id']}", 'source canonical does not match HTTPS base host')
        post_tasks, post_unavailable = publication_plan(post, manifest)
        tasks.extend(post_tasks)
        expected_urls.update(task[1] for task in post_tasks)
        unavailable_urls.update(post_unavailable)
    resources = set()
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        for found in pool.map(lambda task: public_document(client, *task), tasks):
            resources.update(found)
        private_paths = [f"{prefix}/?{'page_id' if post['type'] == 'page' else 'p'}={post['id']}"
                         for post in private for _, prefix, _ in LANGUAGES]
        for post in private:
            for _, prefix, _ in LANGUAGES:
                key = 'page_id' if post['type'] == 'page' else 'p'
                private_urls.add(urllib.parse.urlunsplit((client.base.scheme, client.base.netloc,
                                                        prefix + '/', f"{key}={post['id']}", '')))
                if post.get('canonical_url'):
                    private_urls.add(translated_url(post['canonical_url'], prefix))
        list(pool.map(lambda path: require_status(client, path, 404), private_paths))
        list(pool.map(lambda value: require_status(client, route_for(value), 404), sorted(unavailable_urls)))

    original_post = next((post for post in public if post['type'] == 'post'), public[0])
    original = original_post['canonical_url']
    links = publication_links(original_post, manifest)
    # The legacy PHP entry exists only at the original root. Translated pages
    # use /en/?p=..., /nl/?p=... and /es/?p=..., already covered above.
    path = '/index.php?' + urllib.parse.urlsplit(original).query
    resources.update(public_document(client, path, original, links, 'zh-Hans'))

    robots_response = require_status(client, '/robots.txt', 200)
    publicly_indexable(robots_response, '/robots.txt')
    robots = robots_response.body.decode('utf-8')
    disallows = {line.split(':', 1)[1].strip() for line in robots.splitlines()
                 if line.lower().startswith('disallow:')}
    require('/' not in disallows and any(value in ('/admin', '/admin/') for value in disallows),
            '/robots.txt', 'public indexing/admin exclusion incorrect')
    index_url = args.base_url.rstrip('/') + '/?sitemap=index'
    require(any(line.strip() == 'Sitemap: ' + index_url for line in robots.splitlines()),
            '/robots.txt', 'production sitemap declaration missing')
    index = xml_response(client, '/?sitemap=index', 'sitemapindex')
    namespace = {'s': 'http://www.sitemaps.org/schemas/sitemap/0.9'}
    index_locations = {element.text for element in index.findall('s:sitemap/s:loc', namespace)}
    sitemap_urls = set()
    for kind in ('posts-post-1', 'posts-page-1'):
        path = '/?sitemap=' + kind
        require(args.base_url.rstrip('/') + path in index_locations, path,
                'article/page sitemap absent from index')
        sitemap = xml_response(client, path, 'urlset')
        sitemap_urls.update(element.text for element in sitemap.findall('s:url/s:loc', namespace))
    require(expected_urls.issubset(sitemap_urls), '/?sitemap=index',
            'published language URLs missing from sitemaps')
    require(not private_urls.intersection(sitemap_urls), '/?sitemap=index',
            'private URLs exposed in sitemaps')
    require(not unavailable_urls.intersection(sitemap_urls), '/?sitemap=index',
            'unpublished or outdated translations exposed in sitemaps')
    private_ids = {str(post['id']) for post in private}
    sitemap_extras = sitemap_extra_urls(client, sitemap_urls, expected_urls,
                                       private_urls, private_ids, '/?sitemap=index')
    extra_expected, extra_unavailable = extra_publication_plan(sitemap_extras, manifest)
    require(extra_expected.issubset(sitemap_urls), '/?sitemap=index',
            'new published language URLs missing from sitemaps')
    require(not extra_unavailable.intersection(sitemap_urls), '/?sitemap=index',
            'new unpublished or outdated translations exposed in sitemaps')
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        list(pool.map(lambda value: require_status(client, route_for(value), 404), sorted(extra_unavailable)))
    unavailable_urls.update(extra_unavailable)
    for _, prefix, _ in LANGUAGES:
        for feed in ('rss2', 'comments-rss2'):
            path = prefix + '/?feed=' + feed
            response = require_status(client, path, 410)
            require('noindex' in response.headers.get('X-Robots-Tag', '').lower(),
                    path, 'retired feed lacks noindex')
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        for found in pool.map(lambda value: sitemap_extra_document(client, value), sorted(sitemap_extras)):
            resources.update(found)

    admin_path = '/admin/'
    admin = require_status(client, admin_path, 200)
    anonymous_only(admin, admin_path)
    session_path = '/admin/api/session'
    session = require_status(client, session_path, 200)
    anonymous_only(session, session_path)
    try:
        payload = json.loads(session.body)
    except (ValueError, UnicodeError) as error:
        raise CheckFailed(f'{session_path}: invalid anonymous session JSON') from error
    require(isinstance(payload, dict) and set(payload) == {'authenticated', 'csrf', 'configured'}
            and payload['authenticated'] is False and payload['configured'] is True
            and isinstance(payload['csrf'], str) and bool(payload['csrf']),
            session_path, 'anonymous session exposes fields or admin is unconfigured')
    protected_api = ('/admin/api/posts', '/admin/api/post?id=' + str(original_post['id']),
                     '/admin/api/settings', '/admin/api/media',
                     '/admin/api/translation-config', '/admin/api/queue')
    for path in protected_api:
        response = require_status(client, path, 401)
        anonymous_only(response, path)
        try:
            payload = json.loads(response.body)
        except (ValueError, UnicodeError) as error:
            raise CheckFailed(f'{path}: invalid unauthenticated error JSON') from error
        require(isinstance(payload, dict) and set(payload) == {'error'}
                and isinstance(payload['error'], str), path,
                'unauthenticated API exposes non-error data')

    blocked_paths = ('/this-page-does-not-exist', '/?p=99999999', '/wp-config.php',
                     '/wp-admin/', '/wp-login.php', '/data/blog.db', '/data/runtime.env',
                     '/data/admin-access.txt', '/data/translation.key',
                     '/data/wordpress-original.json', '/deploy/ganwen-blog.service',
                     '/ganwen-go/data/blog.db', '/migration/wordpress.json', '/README.md')
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        blocked_results = list(pool.map(lambda path: blocked_path_check(client, path), blocked_paths))
    cf_edge_blocks = [{'path': path, 'status': status, 'origin_status_verified': False}
                      for path, status in blocked_results if status == 403]
    language_count = language_checks(client, route_for(original))
    taxonomy_count, taxonomy_resources = taxonomy_checks(client, source, public, args.workers, manifest)
    resources.update(taxonomy_resources)
    resource_paths = set()
    for value in resources:
        path = local_resource_path(value, client)
        if path:
            resource_paths.add(path)
    resource_paths.update(original_media)
    # Media, especially video, is HEAD only. Cloudflare's injected email JS
    # returns 404 for HEAD even when its small GET response is available.
    cloudflare_scripts = {path for path in resource_paths if cloudflare_email_script(path)}
    def resource_check(path):
        method = 'GET' if path in cloudflare_scripts else 'HEAD'
        response = client.fetch(path, method)
        require(response.status == 200, path,
                f'media/assets {method} expected 200, got {response.status}')
        if path in cloudflare_scripts:
            require(len(response.body) <= 65536 and 'javascript' in
                    response.headers.get('Content-Type', '').lower(), path,
                    'Cloudflare email decoder is not a small JavaScript response')
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        list(pool.map(resource_check, sorted(resource_paths)))
    return {'status': 'passed', 'published_articles_and_pages': len(public),
            'public_language_pages': len(tasks), 'private_articles_and_pages': len(private),
            'private_language_404s': len(private_paths), 'legacy_index_entries': 1,
            'unpublished_translation_404s': len(unavailable_urls),
            'publication_manifest_articles': len(manifest),
            'robots_sitemaps': 4, 'retired_feed_checks': 2 * len(LANGUAGES), 'admin_anonymous_checks': 2 + len(protected_api),
            'sitemap_new_publication_checks': len(sitemap_extras),
            'private_or_unknown_404s': sum(status == 404 for _, status in blocked_results),
            'cf_edge_blocks': cf_edge_blocks, 'homepage_language_checks': language_count,
            'taxonomy_archive_checks': taxonomy_count,
            'media_and_asset_heads': len(resource_paths) - len(cloudflare_scripts),
            'original_media_head_checks': len(original_media),
            'cloudflare_email_script_gets': len(cloudflare_scripts), 'tls_certificate_verified': True,
            'origin_override': bool(args.origin_host)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='https://www.ganwen.nl')
    parser.add_argument('--source', required=True, help='UTF-8 original WordPress JSON snapshot')
    parser.add_argument('--media-manifest', help='UTF-8 original media manifest; verify every upload with HEAD')
    parser.add_argument('--publication-manifest',
                        help='UTF-8 mapping of article IDs to currently published language codes; omitted IDs retain all-language checks')
    parser.add_argument('--origin-host', help='Connect to this IP while retaining HTTPS hostname/SNI')
    parser.add_argument('--workers', type=int, default=4, choices=range(1, 9))
    parser.add_argument('--timeout', type=float, default=20)
    args = parser.parse_args()
    if args.timeout <= 0 or args.timeout > 120:
        parser.error('timeout must be greater than zero and no more than 120 seconds')
    try:
        summary = verify(args)
    except CheckFailed as error:
        print(json.dumps({'status': 'failed', 'check': str(error)}, ensure_ascii=False))
        return 1
    except (ValueError, KeyError, TypeError, OSError) as error:
        # Do not serialize source/response data or a sensitive filesystem exception.
        print(json.dumps({'status': 'failed', 'check': 'invalid input or unreadable source',
                          'error_type': type(error).__name__}, ensure_ascii=False))
        return 1
    print(json.dumps(summary, ensure_ascii=False))
    return 0


if __name__ == '__main__':
    sys.exit(main())
