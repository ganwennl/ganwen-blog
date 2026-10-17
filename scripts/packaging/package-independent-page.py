"""Package a verified independent-page migration without runtime data or secrets."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
DEPLOY_FILES = ("ganwen-go.bt.conf.example", "ganwen.nl.bt.conf.example", "nginx-locations.conf.example")
ARCHIVE_ROOT = "ganwen-independent-page"

INSTALL = """# 独立证据页增量安装说明

本包仅含新 Linux amd64 程序、独立页原文、已校验媒体和部署参考；没有数据库、管理员配置、运行配置、API Key、翻译密钥或恢复私钥。
只在用户明确授权上线后，通过宝塔面板执行。现有博客数据必须保留，不用本包重建数据库，也不覆盖整个 data 目录。

1. 在宝塔中停止 Go 项目 `ganwen_blog`，在服务器私密目录备份现有程序、完整 data 目录及 Nginx 配置。保留原 `/www/wwwroot/ganwen.nl/bewijs.html` 和旧媒体。
2. 通过宝塔上传并解压到新的私密暂存目录。不要解压进旧网站公开目录，暂存目录也不要配置为网站根目录。
3. 在宝塔终端进入解压后的 `ganwen-independent-page` 目录，以本包新程序导入已完整验证的证据：

```sh
chmod 750 ./ganwen
./ganwen import-independent-page --data /www/wwwroot/ganwen-go/data --media /www/wwwroot/ganwen-go/data/media --file ./prepared/page.json
```

   相同记录和文件会跳过；已修改的记录、地址或不同媒体会拒绝覆盖。任何校验失败时停止上线，不清除原数据。
4. 仅用本包 `ganwen` 替换 `/www/wwwroot/ganwen-go/ganwen`。保留服务器的 runtime.env、数据库、全部媒体、管理员配置及所有密钥。
5. 用 deploy 下模板核对现有宝塔 Nginx 配置：`/bewijs.html` 及语言路径转发 Go；媒体优先 Go，仅在真实 404 时回退旧目录。保留现有域名、端口、证书及宝塔必要配置。实际 Go 项目模板是 `ganwen-go.bt.conf.example`，监听 127.0.0.1:8099。
6. 通过宝塔检查 Nginx 配置后，启动新 Go 项目并重载 Nginx。检查独立页、中文和荷兰语、返回博客、所有证据、首页/搜索/站点地图隐藏及普通博客媒体。

回滚必须停止项目，再恢复上线前的数据快照、原程序及原 Nginx 配置；仅回退旧程序不能保留新独立页的隐藏规则。
本包的 `package-manifest.json` 包含除清单自身外的所有文件大小与 SHA-256；生成工具还读取完成后的整个归档，逐个核对文件和清单。
"""


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def regular(path, boundary=None):
    path = Path(path)
    if not path.is_file() or path.is_symlink():
        raise ValueError("Package input must be a regular file")
    if boundary is not None:
        boundary = Path(boundary).absolute()
        if not path.absolute().is_relative_to(boundary):
            raise ValueError("Package input escapes its allowed directory")
        current = path.absolute()
        while current != boundary.parent:
            info = current.lstat()
            if current.is_symlink() or getattr(info, "st_file_attributes", 0) & 0x400:
                raise ValueError("Package input contains a reparse point")
            current = current.parent
    return path


def load_preparer(root):
    location = root / "scripts/migration/prepare-independent-page.py"
    spec = importlib.util.spec_from_file_location("independent_preparer", location)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verified_prepared_files(prepared, preparer):
    source = regular(prepared / "source.html", prepared)
    page = regular(prepared / "page.json", prepared)
    package = json.loads(page.read_text(encoding="utf-8"))
    if set(package) != {"schema", "source_url", "post", "source", "media"} or package["schema"] != 1:
        raise ValueError("Unexpected independent-page package metadata")
    if set(package["source"]) != {"path", "bytes", "sha256"} or package["source"]["path"] != "source.html":
        raise ValueError("Invalid original-source manifest")
    raw = source.read_bytes()
    post, paths = preparer.extract_page(raw, package["source_url"], package["post"]["public_path"])
    if urllib.parse.urlsplit(package["source_url"]).path != post["public_path"]:
        raise ValueError("Original page address differs from the migrated page")
    if package["source"]["bytes"] != len(raw):
        raise ValueError("Original HTML size differs from its manifest")
    preparer.verify_package(prepared, raw, post, paths)
    files = {"prepared/source.html": source, "prepared/page.json": page}
    for entry in package["media"]:
        if set(entry) != {"path", "bytes", "sha256"} or preparer.safe_relative_path(entry["path"]) != entry["path"]:
            raise ValueError("Invalid evidence manifest entry")
        relative = "prepared/media/" + entry["path"]
        files[relative] = regular(prepared / "media" / entry["path"], prepared)
    return package, files


def build_package(prepared_dir, binary_path, output_path, project_root=ROOT):
    root = Path(project_root).resolve()
    dist = root / "dist"
    dist.mkdir(parents=True, exist_ok=True)
    if dist.is_symlink() or getattr(dist.lstat(), "st_file_attributes", 0) & 0x400:
        raise ValueError("Delivery directory cannot be a reparse point")
    output = Path(output_path).absolute()
    if output.parent.resolve() != dist or output.exists() or output.is_symlink() or not output.name.endswith(".tar.gz"):
        raise ValueError("Output must be a new .tar.gz file directly inside project dist")
    binary = regular(Path(binary_path).absolute())
    with binary.open("rb") as source:
        header = source.read(20)
    if len(header) != 20 or header[:6] != b"\x7fELF\x02\x01" or int.from_bytes(header[18:20], "little") != 62:
        raise ValueError("Deployment program must be Linux amd64")
    preparer = load_preparer(root)
    prepared = Path(prepared_dir).absolute()
    package, files = verified_prepared_files(prepared, preparer)
    files["ganwen"] = binary
    files["README.md"] = regular(root / "README.md", root)
    for name in DEPLOY_FILES:
        files["deploy/" + name] = regular(root / "deploy" / name, root)
    # Whitelist every input. Runtime directories and arbitrary preparation files
    # never participate in this incremental archive.
    with tempfile.TemporaryDirectory(prefix=".independent-package-", dir=dist) as temporary:
        temporary_root = Path(temporary).resolve()
        if not temporary_root.is_relative_to(dist):
            raise ValueError("Package staging directory escapes delivery directory")
        staging = temporary_root / ARCHIVE_ROOT
        staging.mkdir(mode=0o700)
        expected = {}
        for relative, source in sorted(files.items()):
            target = staging / relative
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            expected[relative] = {"bytes": source.stat().st_size, "sha256": digest(source)}
            shutil.copyfile(source, target)
            if target.stat().st_size != expected[relative]["bytes"] or digest(target) != expected[relative]["sha256"]:
                raise ValueError("Package input changed during copying")
        installation = staging / "INSTALL.md"
        installation.write_text(INSTALL, encoding="utf-8", newline="\n")
        expected["INSTALL.md"] = {"bytes": installation.stat().st_size, "sha256": digest(installation)}
        # Recheck the copied preparation, including every source reference. A
        # concurrent edit cannot produce an archive with mismatched evidence.
        verified_prepared_files(staging / "prepared", preparer)
        manifest = {"schema": 1, "files": expected, "evidence_files": len(package["media"]),
                    "evidence_bytes": sum(entry["bytes"] for entry in package["media"])}
        manifest_path = staging / "package-manifest.json"
        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        expected["package-manifest.json"] = {"bytes": manifest_path.stat().st_size, "sha256": digest(manifest_path)}
        with tarfile.open(output, "x:gz", compresslevel=6) as archive:
            for source in [staging] + sorted(staging.rglob("*")):
                relative = str(source.relative_to(temporary_root)).replace("\\", "/")
                info = archive.gettarinfo(str(source), arcname=relative)
                if not info.isfile() and not info.isdir():
                    raise ValueError("Unexpected special file in delivery package")
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                info.mode = 0o700 if info.isdir() else (0o750 if source == staging / "ganwen" else 0o600)
                if info.isfile():
                    with source.open("rb") as stream:
                        archive.addfile(info, stream)
                else:
                    archive.addfile(info)
        checked = {}
        with tarfile.open(output, "r:gz") as archive:
            for member in archive.getmembers():
                if member.isdir():
                    continue
                if not member.isfile() or not member.name.startswith(ARCHIVE_ROOT + "/"):
                    raise ValueError("Unexpected archive entry")
                with archive.extractfile(member) as stream:
                    relative = member.name.removeprefix(ARCHIVE_ROOT + "/")
                    if relative in checked:
                        raise ValueError("Duplicate archive entry")
                    checked[relative] = {"bytes": member.size, "sha256": hashlib.file_digest(stream, "sha256").hexdigest()}
        if checked != expected:
            raise ValueError("Completed archive checksum verification failed")
    os.chmod(output, 0o600)
    return {"package": str(output), "bytes": output.stat().st_size, "sha256": digest(output),
            "verified_files": len(expected), "evidence_files": len(package["media"]),
            "evidence_bytes": sum(entry["bytes"] for entry in package["media"])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepared-dir", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(build_package(args.prepared_dir, args.binary, args.output), ensure_ascii=False))
    except Exception as error:
        print(json.dumps({"packaged": False, "error_type": type(error).__name__}, ensure_ascii=False))
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
