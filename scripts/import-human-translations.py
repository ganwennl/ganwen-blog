"""Validate and atomically import reviewed translations into an existing SQLite blog.

The source JSON is a read-only snapshot of the posts being reviewed.
Each language JSON contains id/title/content_html/excerpt_html/seo, with no secrets.
Dry-run is the default. Stop the application before using --apply in production.
"""

import argparse
import copy
import datetime
import hashlib
import json
import os
import pathlib
import re
import secrets
import sqlite3
from html.parser import HTMLParser

LANGUAGES = ("nl", "en", "hi", "es", "ar", "fr", "bn", "pt", "id", "ur")


class HTMLShape(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.events = []
        self.text = []

    def handle_starttag(self, tag, attrs):
        # Only human-readable titles and alternative text may change.
        self.events.append(("start", tag, sorted((k, v) for k, v in attrs if k not in ("alt", "title"))))
        self.text.extend(v for k, v in attrs if k in ("alt", "title") and v)

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)

    def handle_endtag(self, tag):
        self.events.append(("end", tag))

    def handle_data(self, text):
        self.text.append(text)


def load_json(path):
    return json.loads(pathlib.Path(path).read_text(encoding="utf-8"))


def source_hash(post):
    return hashlib.sha256((post["title"] + "\0" + post["content_html"] + "\0" + post["excerpt_html"]).encode("utf-8")).hexdigest()


def validate(source, directory, languages=LANGUAGES, include_unpublished=False, allowed_source_phrases=None):
    originals = {p["id"]: p for p in source}
    assert len(originals) == len(source) and originals, "Source IDs must be unique and nonempty"
    statuses = ("publish", "draft", "private", "pending") if include_unpublished else ("publish",)
    assert all(p["status"] in statuses and p["type"] in ("post", "page") for p in source), "Only eligible posts/pages may be reviewed"
    allowed_source_phrases = allowed_source_phrases or {}
    assert isinstance(allowed_source_phrases, dict), "Source phrase allowlist must be an object"
    assert all(str(pid) in {str(value) for value in originals} for pid in allowed_source_phrases), "Source phrase allowlist has unknown IDs"
    phrases = {}
    for pid, post in originals.items():
        approved = allowed_source_phrases.get(str(pid), allowed_source_phrases.get(pid, []))
        assert isinstance(approved, list) and len(approved) <= 32, f"{pid}: invalid source phrase allowlist"
        shape = HTMLShape()
        shape.feed(post["content_html"])
        shape.feed(post["excerpt_html"])
        source_text = "\n".join([post["title"], *shape.text])
        assert all(isinstance(value, str) and 1 <= len(value) <= 80 and value in source_text
                   and re.search(r"[\u3400-\u9fff]", value) for value in approved), f"{pid}: allowed phrase must occur in the source"
        phrases[pid] = approved
    result = {pid: {} for pid in originals}
    for lang in languages:
        items = load_json(pathlib.Path(directory) / (lang + ".json"))
        assert len(items) == len(originals) and {p["id"] for p in items} == set(originals), f"{lang}: incomplete or duplicate IDs"
        for item in items:
            pid = item["id"]
            assert set(item) == {"id", "title", "content_html", "excerpt_html", "seo"}, f"{lang}/{pid}: unexpected fields"
            assert isinstance(item["title"], str) and item["title"].strip() and len(item["title"]) < 2000, f"{lang}/{pid}: invalid title"
            assert set(item["seo"]) == {"title", "description"}, f"{lang}/{pid}: invalid SEO"
            assert all(isinstance(v, str) for v in item["seo"].values()), f"{lang}/{pid}: invalid SEO text"
            visible = [item["title"], *item["seo"].values()]
            for field in ("content_html", "excerpt_html"):
                assert isinstance(item[field], str) and len(item[field].encode("utf-8")) < 8 << 20, f"{lang}/{pid}: oversized/invalid {field}"
                before, after = HTMLShape(), HTMLShape()
                before.feed(originals[pid][field])
                after.feed(item[field])
                assert before.events == after.events, f"{lang}/{pid}/{field}: HTML structure, attributes, links or media changed"
                assert sum(bool(t.strip()) for t in before.text) == sum(bool(t.strip()) for t in after.text), f"{lang}/{pid}/{field}: missing text nodes"
                visible.extend(after.text)
            foreign_text = "".join(visible)
            for phrase in sorted(phrases[pid], key=len, reverse=True):
                foreign_text = foreign_text.replace(phrase, "")
            assert not re.search(r"[\u3400-\u9fff]", foreign_text), f"{lang}/{pid}: untranslated Chinese text"
            assert item["content_html"].strip() or not originals[pid]["content_html"].strip(), f"{lang}/{pid}: empty content"
            result[pid][lang] = {k: v for k, v in item.items() if k != "id"}
    return result


def import_translations(database, source, translations, backup, apply, execution_mode="paused", include_unpublished=False):
    assert execution_mode in ("paused", "manual"), "Importer requires a nonautomatic execution mode"
    statuses = ("publish", "draft", "private", "pending") if include_unpublished else ("publish",)
    assert all(p["type"] in ("post", "page") and p["status"] in statuses for p in source), "Only eligible posts/pages may be imported"
    connection = sqlite3.connect(str(database), timeout=20)
    try:
        if apply:
            assert backup is not None, "--backup is required with --apply"
            backup = pathlib.Path(backup)
            backup.mkdir(mode=0o700, parents=True, exist_ok=False)
            os.chmod(backup, 0o700)
            target = sqlite3.connect(str(backup / "before.sqlite"))
            try:
                connection.backup(target)
            finally:
                target.close()
            os.chmod(backup / "before.sqlite", 0o600)
        connection.execute("BEGIN IMMEDIATE")
        try:
            assert not connection.execute("SELECT id FROM translation_jobs WHERE status IN ('queued','retry','running') LIMIT 1").fetchone(), "Active translation jobs; stop/pause before importing"
            now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            before_documents = []
            for expected in source:
                pid = expected["id"]
                row = connection.execute("SELECT document FROM posts WHERE id=?", (pid,)).fetchone()
                assert row, f"{pid}: source post missing"
                raw = row[0]
                before = json.loads(raw)
                assert before == expected, f"{pid}: post changed since review snapshot; reread before importing"
                before_documents.append(before)
                after = copy.deepcopy(before)
                for lang, content in translations[pid].items():
                    after.setdefault("translations", {})[lang] = {
                        **content, "source_hash": source_hash(before),
                        "status": "publish" if before["status"] == "publish" else "review", "updated_at": now
                    }
                after["version"] += 1
                assert {k: v for k, v in after.items() if k not in ("translations", "version")} == {k: v for k, v in before.items() if k not in ("translations", "version")}, f"{pid}: original fields changed"
                encoded = json.dumps(after, ensure_ascii=False, separators=(",", ":"))
                updated = connection.execute("UPDATE posts SET document=? WHERE id=? AND document=?", (encoded, pid, raw))
                assert updated.rowcount == 1, f"{pid}: concurrent update"
            # Use the same transactional revision contract as Go writers so
            # other running readers cannot retain the old decoded articles.
            connection.execute(
                "INSERT INTO kv(key,value) VALUES('posts_revision_v1',?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (secrets.token_hex(16),))
            revision_row = connection.execute(
                "SELECT key FROM kv WHERE key='posts_revision_v1'").fetchone()
            if revision_row is None or revision_row[0] != 'posts_revision_v1':
                raise ValueError('Article cache revision key collides with existing configuration')
            config_row = connection.execute("SELECT value FROM kv WHERE key='translation_config_v1'").fetchone()
            assert config_row, "Persisted translation configuration is required"
            old_config = json.loads(config_row[0])
            config = {**old_config, "execution_mode": execution_mode}
            connection.execute("UPDATE kv SET value=? WHERE key='translation_config_v1' AND value=?", (json.dumps(config, ensure_ascii=False, separators=(",", ":")), config_row[0]))
            if apply:
                # Recovery data contains encrypted config; never put the backup in Git or a public directory.
                recovery = backup / "reviewed-posts-before.json"
                recovery.write_text(json.dumps(before_documents, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
                os.chmod(recovery, 0o600)
                connection.commit()
            else:
                connection.rollback()
            return {"applied": apply, "posts": len(source), "translations": sum(len(v) for v in translations.values()), "external_translation": execution_mode, "originals_unchanged": True}
        except BaseException:
            connection.rollback()
            raise
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", type=pathlib.Path)
    parser.add_argument("--source", required=True, type=pathlib.Path)
    parser.add_argument("--translations", required=True, type=pathlib.Path)
    parser.add_argument("--backup", type=pathlib.Path)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--include-unpublished", action="store_true",
                        help="Review drafts/private/pending originals without making them public")
    parser.add_argument("--allow-source-phrases", type=pathlib.Path,
                        help="Explicit per-post source phrases kept as named quotations; other Chinese still fails")
    parser.add_argument("--execution-mode", choices=("paused", "manual"), default="paused",
                        help="Pause all service calls, or allow explicit clicks only; never enable automatic calls")
    args = parser.parse_args()
    source = load_json(args.source)
    phrases = load_json(args.allow_source_phrases) if args.allow_source_phrases else None
    translations = validate(source, args.translations, include_unpublished=args.include_unpublished,
                            allowed_source_phrases=phrases)
    if args.database:
        assert args.database.is_file(), "Database must already exist"
        result = import_translations(args.database, source, translations, args.backup, args.apply,
                                     args.execution_mode, args.include_unpublished)
    else:
        assert not args.apply, "--database is required with --apply"
        result = {"validated": True, "posts": len(source), "translations": sum(len(v) for v in translations.values())}
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
