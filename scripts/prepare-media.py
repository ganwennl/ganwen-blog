"""Extract an authorized migration archive and verify every original file."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import tarfile

parser = argparse.ArgumentParser()
parser.add_argument("--archive", default="migration/media.tar.gz")
parser.add_argument("--manifest", default="migration/media-manifest.json")
parser.add_argument("--destination", default="data/media")
parser.add_argument("--aliases", default=str(Path(__file__).with_name("media-aliases.json")))
args = parser.parse_args()
destination = Path(args.destination).resolve()
destination.mkdir(parents=True, exist_ok=True)
manifest = json.loads(Path(args.manifest).read_text(encoding="utf-8"))
expected = {entry["path"]: entry for entry in manifest}
with tarfile.open(args.archive, "r:gz", encoding="utf-8") as archive:
    for member in archive.getmembers():
        target = (destination / member.name).resolve()
        if not target.is_relative_to(destination) or not member.isfile():
            raise ValueError(f"Unsafe archive entry: {member.name}")
        if member.name not in expected:
            raise ValueError(f"Unlisted archive entry: {member.name}")
    archive.extractall(destination, filter="data")
for name, entry in expected.items():
    target = (destination / name).resolve()
    if not target.is_relative_to(destination):
        raise ValueError("Manifest path escapes destination")
    data = target.read_bytes()
    if len(data) != entry["bytes"] or hashlib.sha256(data).hexdigest() != entry["sha256"]:
        raise ValueError(f"Media mismatch: {name}")
aliases = json.loads(Path(args.aliases).read_text(encoding="utf-8"))
for entry in aliases:
    target = (destination / entry["path"]).resolve()
    fallback = (destination / entry["fallback"]).resolve()
    if not target.is_relative_to(destination) or not fallback.is_relative_to(destination):
        raise ValueError("Media alias escapes destination")
    if entry["fallback"] not in expected or hashlib.sha256(fallback.read_bytes()).hexdigest() != entry["sha256"]:
        raise ValueError("Media alias source mismatch")
    if target.exists():
        # Keep a recovered original if a future snapshot contains it.
        if entry["path"] not in expected and hashlib.sha256(target.read_bytes()).hexdigest() != entry["sha256"]:
            raise ValueError("Existing unverified media alias")
        continue
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(fallback, target)
print(json.dumps({"verified_files": len(expected), "verified_bytes": sum(e["bytes"] for e in manifest), "missing_original_thumbnail_aliases": len(aliases)}, ensure_ascii=False))
