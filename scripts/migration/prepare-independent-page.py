"""Prepare a verified, private migration package for a bilingual standalone page.

The original HTML is retained verbatim. Evidence stays outside Git; only same-site
media is fetched, while citations remain links. Existing packages are never replaced.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
from html import escape
from html.parser import HTMLParser
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import tempfile
import urllib.parse
import urllib.request

VOID_TAGS = set("area base br col embed hr img input link meta param source track wbr".split())
DROP_TAGS = set("script style form object embed input button link meta".split())
MEDIA_EXTENSIONS = set(".jpg .jpeg .png .gif .webp .avif .heic .heif .ico .mp4 .m4v .mov .webm .mp3 .m4a .ogg .wav .pdf".split())
PRESERVE_CLASSES = set("timeline-item timeline-dot highlight-box note-box overflow-x-auto".split())


class Node:
    def __init__(self, tag="root", attrs=()):
        self.tag, self.attrs, self.children = tag, dict(attrs), []

    def text(self):
        return "".join(child if isinstance(child, str) else child.text() for child in self.children)


class Document(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.root = Node()
        self.stack = [self.root]

    def handle_starttag(self, tag, attrs):
        node = Node(tag, attrs)
        self.stack[-1].children.append(node)
        if tag not in VOID_TAGS:
            self.stack.append(node)

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)
        if tag not in VOID_TAGS:
            self.handle_endtag(tag)

    def handle_endtag(self, tag):
        for index in range(len(self.stack) - 1, 0, -1):
            if self.stack[index].tag == tag:
                self.stack = self.stack[:index]
                break

    def handle_data(self, data):
        self.stack[-1].children.append(data)


def walk(node):
    yield node
    for child in node.children:
        if isinstance(child, Node):
            yield from walk(child)


def safe_relative_path(path):
    decoded = urllib.parse.unquote(path)
    relative = decoded.lstrip("/")
    parts = relative.split("/")
    if not relative or "\\" in relative or any(part in {".", ".."} or ":" in part for part in parts):
        raise ValueError("Unsafe media path")
    if any(ord(character) < 32 for character in relative):
        raise ValueError("Unsafe media path")
    return relative


def source_hosts(source_url):
    parsed = urllib.parse.urlsplit(source_url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError("The original page must use HTTPS")
    host = parsed.hostname.lower()
    return {host, host[4:] if host.startswith("www.") else "www." + host}


def local_reference(value, source_url, hosts):
    # Anchors must stay anchors; resolving them against the old URL would turn
    # translated-page evidence links into links back to the Chinese page.
    if value.startswith("#"):
        return value
    parsed = urllib.parse.urlsplit(urllib.parse.urljoin(source_url, value))
    if parsed.hostname and parsed.hostname.lower() in hosts and parsed.scheme in {"https", "http"}:
        return urllib.parse.urlunsplit(("", "", parsed.path, parsed.query, parsed.fragment))
    return value


def render(node, source_url, hosts):
    if isinstance(node, str):
        return escape(node, quote=False)
    if node.tag in DROP_TAGS:
        return ""
    attrs = []
    for key, value in node.attrs.items():
        if key.startswith("on") or key == "style":
            continue
        if key == "class":
            classes = [item for item in (value or "").split() if item in PRESERVE_CLASSES]
            if "grid" in (value or "").split():
                classes.append("evidence-gallery")
            if not classes:
                continue
            value = " ".join(classes)
        if key in {"src", "href", "poster"} and value is not None:
            value = local_reference(value, source_url, hosts)
        attrs.append(" " + key if value is None else ' ' + key + '="' + escape(value, quote=True) + '"')
    opening = "<" + node.tag + "".join(attrs) + ">"
    if node.tag in VOID_TAGS:
        return opening
    return opening + "".join(render(child, source_url, hosts) for child in node.children) + "</" + node.tag + ">"


def source_hash(post):
    return hashlib.sha256((post["title"] + "\0" + post["content_html"] + "\0" + post["excerpt_html"]).encode("utf-8")).hexdigest()


def extract_page(raw, source_url, public_path):
    document = Document()
    document.feed(raw.decode("utf-8"))
    nodes = list(walk(document.root))
    hosts = source_hosts(source_url)
    languages = {}
    for language in ("zh", "nl"):
        containers = [node for node in nodes if node.attrs.get("id") == "lang-" + language]
        headers = [node for node in nodes if node.attrs.get("id") == "header-" + language]
        if len(containers) != 1 or len(headers) != 1:
            raise ValueError("Expected one original header and body for each language")
        headings = [node for node in walk(headers[0]) if node.tag == "h1"]
        if len(headings) != 1 or not headings[0].text().strip():
            raise ValueError("Original page title is missing")
        subtitle = "".join(render(child, source_url, hosts) for child in headers[0].children if not isinstance(child, Node) or child.tag != "h1")
        body = "".join(render(child, source_url, hosts) for child in containers[0].children)
        languages[language] = {"title": headings[0].text().strip(), "content_html": '<div class="independent-evidence">' + subtitle + body + "</div>", "excerpt_html": "", "seo": {"title": "", "description": ""}}
    post = {"type": "page", "status": "publish", "public_path": public_path, "unlisted": True,
            "slug": PurePosixPath(public_path).name.removesuffix(".html"), **languages["zh"],
            "source_html": raw.decode("utf-8"), "translations": {}}
    post["translations"]["nl"] = {**languages["nl"], "source_hash": source_hash(post), "status": "publish", "updated_at": ""}
    media = set()
    for language in languages.values():
        body = Document()
        body.feed(language["content_html"])
        for node in walk(body.root):
            for attr in ("src", "href", "poster"):
                value = node.attrs.get(attr)
                if not value or value.startswith("#"):
                    continue
                parsed = urllib.parse.urlsplit(urllib.parse.urljoin(source_url, value))
                if parsed.hostname in hosts and PurePosixPath(parsed.path).suffix.lower() in MEDIA_EXTENSIONS:
                    media.add(safe_relative_path(parsed.path))
    if not media:
        raise ValueError("No original evidence media was found")
    return post, sorted(media)


class SameSiteRedirect(urllib.request.HTTPRedirectHandler):
    def __init__(self, hosts):
        self.hosts = hosts

    def redirect_request(self, request, fp, code, message, headers, new_url):
        parsed = urllib.parse.urlsplit(new_url)
        if parsed.scheme != "https" or parsed.hostname not in self.hosts:
            raise ValueError("Cross-site or insecure redirect refused")
        return super().redirect_request(request, fp, code, message, headers, new_url)


def read_https(url, hosts, target=None):
    request = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0", "Accept-Encoding": "identity"})
    opener = urllib.request.build_opener(SameSiteRedirect(hosts))
    with opener.open(request, timeout=90) as response:
        if response.status != 200:
            raise ValueError("Original resource returned an unsuccessful status")
        if target is None:
            raw = response.read(8 * 1024 * 1024 + 1)
            if len(raw) > 8 * 1024 * 1024:
                raise ValueError("Original HTML exceeds the preparation limit")
            return raw
        if response.headers.get_content_type() in {"text/html", "application/json"}:
            raise ValueError("Original media returned a document instead of evidence")
        with target.open("xb") as output:
            shutil.copyfileobj(response, output)
        declared = response.headers.get("Content-Length")
        if declared and target.stat().st_size != int(declared):
            raise ValueError("Original evidence download is incomplete")


def digest_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_private_json(path, value):
    with path.open("x", encoding="utf-8", newline="\n") as output:
        json.dump(value, output, ensure_ascii=False, indent=2)
        output.write("\n")
    os.chmod(path, 0o600)


def verify_package(destination, raw, post, media_paths):
    package = json.loads((destination / "page.json").read_text(encoding="utf-8"))
    if package["post"] != post or package["source"]["sha256"] != hashlib.sha256(raw).hexdigest():
        raise ValueError("Existing package differs; use a new private staging directory")
    if (destination / "source.html").read_bytes() != raw:
        raise ValueError("Retained original HTML differs")
    if sorted(entry["path"] for entry in package["media"]) != media_paths:
        raise ValueError("Existing evidence manifest is incomplete")
    for entry in package["media"]:
        target = destination / "media" / entry["path"]
        if target.stat().st_size != entry["bytes"] or digest_file(target) != entry["sha256"]:
            raise ValueError("Existing evidence checksum mismatch")
    return package


def prepare(destination, source_url, public_path, source_file=None, existing_media=None, existing_manifest=None):
    destination = Path(destination).resolve()
    hosts = source_hosts(source_url)
    raw = Path(source_file).read_bytes() if source_file else read_https(source_url, hosts)
    post, media_paths = extract_page(raw, source_url, public_path)
    if destination.exists():
        package = verify_package(destination, raw, post, media_paths)
        return {"prepared": False, "verified_files": len(package["media"]), "verified_bytes": sum(entry["bytes"] for entry in package["media"])}
    expected = {}
    if existing_manifest:
        expected = {entry["path"]: entry for entry in json.loads(Path(existing_manifest).read_text(encoding="utf-8"))}
    destination.parent.mkdir(parents=True, exist_ok=True)
    incoming = Path(tempfile.mkdtemp(prefix=destination.name + ".preparing-", dir=destination.parent))
    os.chmod(incoming, 0o700)
    source_target = incoming / "source.html"
    source_target.write_bytes(raw)
    os.chmod(source_target, 0o600)

    def prepare_media(relative):
        target = incoming / "media" / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        local = Path(existing_media).resolve() / relative if existing_media else None
        trusted = expected.get(relative)
        if local and local.is_file() and trusted:
            if local.stat().st_size != trusted["bytes"] or digest_file(local) != trusted["sha256"]:
                raise ValueError("Existing original-media checksum mismatch")
            shutil.copyfile(local, target)
            if target.stat().st_size != trusted["bytes"] or digest_file(target) != trusted["sha256"]:
                raise ValueError("Existing original media changed during copying")
        else:
            parsed = urllib.parse.urlsplit(source_url)
            media_url = urllib.parse.urlunsplit(("https", parsed.netloc, "/" + urllib.parse.quote(relative, safe="/"), "", ""))
            read_https(media_url, hosts, target)
        if target.stat().st_size == 0:
            raise ValueError("Empty original evidence resource")
        os.chmod(target, 0o600)
        return {"path": relative, "bytes": target.stat().st_size, "sha256": digest_file(target)}

    # A failed preparation retains its private incoming directory for recovery;
    # the final package is visible only after all evidence has been verified.
    with ThreadPoolExecutor(max_workers=4) as pool:
        media = list(pool.map(prepare_media, media_paths))
    package = {"schema": 1, "source_url": source_url, "post": post,
               "source": {"path": "source.html", "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}, "media": media}
    write_private_json(incoming / "page.json", package)
    verify_package(incoming, raw, post, media_paths)
    if destination.exists():
        raise ValueError("Staging destination appeared during preparation")
    incoming.rename(destination)
    return {"prepared": True, "verified_files": len(media), "verified_bytes": sum(entry["bytes"] for entry in media), "source_bytes": len(raw)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-url", default="https://www.ganwen.nl/bewijs.html")
    parser.add_argument("--public-path", default="/bewijs.html")
    parser.add_argument("--destination", default="data/bewijs-migration-20261013")
    parser.add_argument("--source-file")
    parser.add_argument("--existing-media", default="data/media")
    parser.add_argument("--existing-manifest", default="migration/media-manifest.json")
    args = parser.parse_args()
    try:
        print(json.dumps(prepare(args.destination, args.source_url, args.public_path, args.source_file, args.existing_media, args.existing_manifest), ensure_ascii=False))
    except Exception as error:
        # Keep private captions, resource names and original content out of logs.
        print(json.dumps({"prepared": False, "error_type": type(error).__name__}, ensure_ascii=False))
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
