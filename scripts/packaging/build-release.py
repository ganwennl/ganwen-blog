#!/usr/bin/env python3
"""Build clean standalone packages; never copy local data or configuration."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[2]
VERSION = "1.0.4"


def go_output(go, *args, env=None):
    return subprocess.check_output([go, *args], cwd=ROOT, env=env, text=True, encoding="utf-8")


def json_stream(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        yield value
        raw = raw.lstrip()[end:]


def collect_licenses(go, stage, env):
    modules = {}
    for package in json_stream(go_output(go, "list", "-deps", "-json", "./cmd/ganwen", env=env)):
        module = package.get("Module", {})
        if module.get("Dir") and not module.get("Main"):
            modules[module["Path"]] = module
    notices = ["# 第三方许可 / Third-party notices", "",
               "本项目自己的代码采用 MIT；第三方组件保留各自原许可证。",
               "Project code is MIT licensed. Third-party components retain their original licenses.", ""]
    license_root = stage / "third-party-licenses"
    license_root.mkdir()
    for name, module in sorted(modules.items()):
        module_dir = Path(module["Dir"])
        files = sorted(p for p in module_dir.iterdir() if p.is_file() and
                       p.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "AUTHORS", "PATENTS")))
        if not any(p.name.upper().startswith(("LICENSE", "COPYING")) for p in files):
            raise RuntimeError(f"Dependency license missing: {name}")
        target = license_root / name.replace("/", "_")
        target.mkdir()
        for file in files:
            shutil.copyfile(file, target / file.name)
        notices.append(f"- {name} {module['Version']}: third-party-licenses/{target.name}/")
    go_license = Path(go_output(go, "env", "GOROOT").strip()) / "LICENSE"
    shutil.copyfile(go_license, license_root / "Go-LICENSE")
    shutil.copyfile(ROOT / "deploy" / "geoip-LICENSE-itbdw.txt", license_root / "itbdw-ip-database-LICENSE.txt")
    notices += ["- Go standard library: third-party-licenses/Go-LICENSE", "",
                "- itbdw/ip-database: third-party-licenses/itbdw-ip-database-LICENSE.txt",
                "IP database data is not included in the release package."]
    (stage / "THIRD_PARTY_NOTICES.md").write_text("\n".join(notices) + "\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--go", default="go")
    parser.add_argument("--targets", nargs="+", choices=["windows-amd64", "linux-amd64"],
                        default=["windows-amd64", "linux-amd64"])
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    artifacts = []
    for target in args.targets:
        name = f"ganwen-blog-v{VERSION}-{target}"
        stage = output / name
        archive = output / f"{name}.zip"
        if stage.exists() or archive.exists():
            raise RuntimeError(f"Refusing to overwrite an existing package: {name}")
        stage.mkdir()
        system, arch = target.split("-")
        env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0")
        binary = stage / ("ganwen.exe" if system == "windows" else "ganwen")
        subprocess.run([args.go, "build", "-trimpath", "-buildvcs=false", "-ldflags",
                        f"-X main.version={VERSION}", "-o", str(binary), "./cmd/ganwen"],
                       cwd=ROOT, env=env, check=True)
        for filename in ["README.md", "CHANGELOG.md", "LICENSE"]:
            shutil.copyfile(ROOT / filename, stage / filename)
        shutil.copytree(ROOT / "docs", stage / "docs")
        launchers = ["Start.ps1", "Start.bat"] if system == "windows" else ["start.sh"]
        for filename in launchers:
            shutil.copyfile(ROOT / "packaging" / ("windows" if system == "windows" else "linux") / filename, stage / filename)
        collect_licenses(args.go, stage, env)
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zipped:
            for file in sorted(stage.rglob("*")):
                if file.is_file():
                    info = zipfile.ZipInfo(str(file.relative_to(output)).replace("\\", "/"),
                                           date_time=(2026, 10, 14, 0, 0, 0))
                    info.compress_type = zipfile.ZIP_DEFLATED
                    mode = 0o755 if file.name in ("ganwen", "start.sh") else 0o644
                    info.external_attr = (0o100000 | mode) << 16
                    zipped.writestr(info, file.read_bytes())
        artifacts.append({"file": archive.name, "bytes": archive.stat().st_size,
                          "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()})
    (output / "SHA256SUMS.txt").write_text("".join(f"{item['sha256']}  {item['file']}\n" for item in artifacts), encoding="utf-8")
    (output / "artifacts.json").write_text(json.dumps({"version": VERSION, "artifacts": artifacts}, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"version": VERSION, "artifacts": artifacts}, indent=2))


if __name__ == "__main__":
    main()
