"""Build a private, self-contained BaoTa bundle from a consistent logical backup."""
import argparse
from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def regular_file(path, boundary=None):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f'Expected a regular file: {path}')
    if boundary is not None:
        if not path.resolve().is_relative_to(boundary.resolve()):
            raise ValueError(f'File escapes the media directory: {path}')
        current = path
        while current != boundary.parent:
            if current.is_symlink() or getattr(current.lstat(), 'st_file_attributes', 0) & 0x400:
                raise ValueError(f'Reparse points are not allowed in media paths: {current}')
            current = current.parent
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--backup', required=True)
    parser.add_argument('--output', default='dist/ganwenblog-bt-' +
                        datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '.tar.gz')
    parser.add_argument('--cli', default='dist/ganwen.exe')
    parser.add_argument('--binary', default='dist/ganwen-linux-amd64')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    os.chdir(root)
    backup_path = regular_file(Path(args.backup).resolve())
    backup = json.loads(backup_path.read_text(encoding='utf-8'))
    if backup.get('format') != 'ganwen-blog-database':
        raise ValueError('Expected a Ganwen logical database backup')
    cli = regular_file(Path(args.cli).resolve())
    binary = regular_file(Path(args.binary).resolve())
    with binary.open('rb') as stream:
        header = stream.read(20)
    if len(header) != 20 or header[:6] != b'\x7fELF\x02\x01' or int.from_bytes(header[18:20], 'little') != 62:
        raise ValueError('The deployment binary must be a Linux amd64 executable')
    output = Path(args.output).resolve()
    if not output.is_relative_to(root / 'dist') or output.exists() or output.is_symlink():
        raise ValueError('Output must be a new file inside this project dist directory')
    for name in ('runtime.env', 'admin-access.txt'):
        regular_file(root / 'data' / name)
    for name in ('wordpress.json', 'media-manifest.json'):
        regular_file(root / 'migration' / name)
    deploy_files = ['BT-GO-PROJECT.md', 'ganwen-go.bt.conf.example',
                    'ganwen-blog.service', 'ganwen.nl.bt.conf.example', 'BT-DEPLOY.md',
                    'geoip-LICENSE-itbdw.txt']
    for name in deploy_files:
        regular_file(root / ('docs/deployment' if name.endswith('.md') else 'deploy') / name)
    manifest = json.loads((root / 'migration/media-manifest.json').read_text(encoding='utf-8'))
    aliases = json.loads((root / 'scripts/migration/media-aliases.json').read_text(encoding='utf-8'))
    expected_media = {}
    media_root = root / 'data/media'
    for record in manifest + aliases:
        relative = Path(record['path'])
        if relative.is_absolute() or '..' in relative.parts:
            raise ValueError('Unsafe media manifest path')
        media = regular_file(media_root / relative, media_root)
        if digest(media) != record['sha256']:
            raise ValueError(f'Media fingerprint mismatch: {relative}')
        expected_media[relative] = media
    for media in media_root.rglob('*'):
        if media.is_symlink() or getattr(media.lstat(), 'st_file_attributes', 0) & 0x400:
            raise ValueError(f'Reparse points are not allowed in the media directory: {media}')
        if media.is_file():
            expected_media[media.relative_to(media_root)] = regular_file(media, media_root)

    staging_parent = Path(tempfile.mkdtemp(prefix='bt-package-', dir=root / 'migration'))
    staging = staging_parent / 'ganwen-go'
    data = staging / 'data'
    data.mkdir(parents=True)
    # Restore into a fresh, closed SQLite database instead of copying a live WAL file.
    environment = {k: v for k, v in os.environ.items() if not k.startswith('GBLOG_')}
    subprocess.run([str(cli), 'database-import', '--database', 'sqlite', '--data', str(data),
                    '--file', str(backup_path)], env=environment, check=True,
                   stdout=subprocess.DEVNULL)
    with closing(sqlite3.connect(data / 'blog.db')) as connection:
        actual = [json.loads(row[0]) for row in connection.execute('SELECT document FROM posts ORDER BY id')]
        if actual != sorted(backup['posts'], key=lambda post: post['id']):
            raise ValueError('Restored package content differs from logical backup')
        # A logical restore generates a new internal cache token.
        values = [{'key': k, 'value': v}
                  for k, v in connection.execute('SELECT key,value FROM kv ORDER BY key')
                  if k != 'posts_revision_v1']
        expected_values = [value for value in backup['values']
                           if value['key'] != 'posts_revision_v1']
        if values != sorted(expected_values, key=lambda value: value['key']):
            raise ValueError('Restored package settings differ from logical backup')
        columns = ('id', 'post_id', 'language', 'source_hash', 'status', 'attempts', 'last_error', 'next_attempt_at', 'baseline_hash')
        jobs = [dict(zip(columns, row)) for row in connection.execute('SELECT ' + ','.join(columns) + ' FROM translation_jobs ORDER BY id')]
        if jobs != sorted(backup['translation_jobs'], key=lambda job: job['id']):
            raise ValueError('Restored package translation jobs differ from logical backup')
        analytics = backup.get('analytics')
        if analytics is not None:
            for field, table, columns in (
                    ('aggregates', 'analytics_aggregates', ('date', 'kind', 'label', 'post_id', 'pv', 'uv')),
                    ('visitors', 'analytics_visitors', ('date', 'hash')),
                    ('events', 'analytics_events', ('date', 'hash'))):
                sql_columns = ['id_hash' if name == 'hash' else name for name in columns]
                rows = [dict(zip(columns, row)) for row in connection.execute(
                    'SELECT ' + ','.join(sql_columns) + ' FROM ' + table)]
                order = lambda row: tuple(row[name] for name in columns)
                if sorted(rows, key=order) != sorted(analytics[field], key=order):
                    raise ValueError('Restored package analytics differ from logical backup')
        if connection.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone()[0] != 0:
            raise ValueError('Restored database checkpoint is busy')
    runtime = (root / 'data/runtime.env').read_text(encoding='utf-8')
    runtime = '\n'.join(line for line in runtime.splitlines()
                        if line.strip().split('=', 1)[0].strip() not in
                        {'GBLOG_DATABASE_DRIVER', 'GBLOG_DATABASE_PATH', 'GBLOG_DATABASE_DSN'})
    (data / 'runtime.env').write_text(runtime + '\nGBLOG_DATABASE_DRIVER=sqlite\nGBLOG_DATABASE_PATH=blog.db\n', encoding='utf-8')
    shutil.copy2(root / 'data/admin-access.txt', data / 'admin-access.txt')
    shutil.copy2(root / 'migration/wordpress.json', data / 'wordpress-original.json')
    shutil.copy2(root / 'migration/media-manifest.json', data / 'media-manifest.json')
    shutil.copy2(root / 'scripts/migration/media-aliases.json', data / 'media-aliases.json')
    geo_root = root / 'data/geoip'
    if geo_root.exists():
        if geo_root.is_symlink() or getattr(geo_root.lstat(), 'st_file_attributes', 0) & 0x400:
            raise ValueError('Reparse points are not allowed in the GeoIP directory')
        for name in ('qqwry.dat', 'qqwry.meta.json', 'ipv6wry.db', 'ipv6wry.meta.json'):
            geo_source = geo_root / name
            if geo_source.exists():
                regular_file(geo_source, geo_root)
                (data / 'geoip').mkdir(exist_ok=True)
                shutil.copy2(geo_source, data / 'geoip' / name)
    for relative, source in expected_media.items():
        destination = data / 'media' / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
    shutil.copy2(binary, staging / 'ganwen')
    (staging / 'deploy').mkdir()
    for name in deploy_files:
        shutil.copy2(root / ('docs/deployment' if name.endswith('.md') else 'deploy') / name, staging / 'deploy' / name)
    shutil.copy2(root / 'README.md', staging / 'README.md')
    files = sorted(path for path in staging.rglob('*') if path.is_file()
                   and not path.name.endswith(('-wal', '-shm')))
    expected = {str(path.relative_to(staging_parent)).replace('\\', '/'): digest(path) for path in files}
    inventory_path = staging / 'deploy/package-manifest.json'
    inventory_path.write_text(json.dumps(expected, indent=2), encoding='utf-8')
    files.append(inventory_path)
    expected[str(inventory_path.relative_to(staging_parent)).replace('\\', '/')] = digest(inventory_path)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(output, 'x:gz', compresslevel=6) as archive:
        for path in [staging] + sorted(staging.rglob('*')):
            if path.is_symlink():
                raise ValueError('Symbolic links are not allowed in the package')
            if path.is_file() and path.name.endswith(('-wal', '-shm')):
                continue
            info = archive.gettarinfo(str(path), arcname=str(path.relative_to(staging_parent)).replace('\\', '/'))
            info.uid = info.gid = 0
            info.uname = info.gname = 'root'
            info.mode = 0o750 if path.is_dir() else (0o755 if path == staging / 'ganwen' else 0o600)
            if path.is_file():
                with path.open('rb') as stream:
                    archive.addfile(info, stream)
            else:
                archive.addfile(info)
    # Read the finished archive back and verify every file, including all original media.
    with tarfile.open(output, 'r:gz') as archive:
        checked = {}
        for member in archive.getmembers():
            if member.isfile():
                with archive.extractfile(member) as stream:
                    checked[member.name] = hashlib.file_digest(stream, 'sha256').hexdigest()
        if checked != expected:
            raise ValueError('Archive verification failed')
    print(json.dumps({'package': str(output), 'bytes': output.stat().st_size,
                      'sha256': digest(output), 'files': len(expected),
                      'original_media': len(manifest), 'thumbnail_aliases': len(aliases),
                      'records': len(actual), 'translations': sum(len(post.get('translations', {})) for post in actual)},
                     ensure_ascii=False))


if __name__ == '__main__':
    main()
