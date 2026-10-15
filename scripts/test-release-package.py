#!/usr/bin/env python3
"""Offline Windows package smoke test with isolated, freshly generated data."""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import time
import urllib.error
import urllib.request
import zipfile


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--package", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--expected-version", default="1.0.2")
    args = parser.parse_args()
    check(os.name == "nt", "This startup test requires Windows")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with zipfile.ZipFile(args.package) as archive:
        names = archive.namelist()
        check(all(not name.startswith(("/", "\\")) and ".." not in Path(name).parts for name in names), "Unsafe archive path")
        check(all("/data/" not in name and "runtime.env" not in name and "admin-access" not in name for name in names), "Private data present in release")
        archive.extractall(output)
    roots = [path for path in output.iterdir() if path.is_dir()]
    check(len(roots) == 1, "Unexpected package layout")
    root = roots[0]
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GBLOG_")}
    version = subprocess.check_output([str(root / "ganwen.exe"), "version"], text=True, encoding="utf-8", env=environment).strip()
    check(version == "Ganwen Blog " + args.expected_version, "Incorrect executable version")
    data = root / "data"
    checks = []
    before_credentials = None
    for attempt in range(2):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        log = (output / f"run-{attempt}.log").open("wb")
        launch = (["cmd.exe", "/d", "/c", str(root / "Start.bat"), "-Port", str(port)] if attempt == 0 else
                  ["powershell.exe", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(root / "Start.ps1"), "-Port", str(port)])
        process = subprocess.Popen(launch, cwd=root,
                                   env=environment, stdout=log, stderr=subprocess.STDOUT,
                                   creationflags=subprocess.CREATE_NO_WINDOW)
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        def request(path, value=None, csrf=""):
            body = None if value is None else json.dumps(value).encode("utf-8")
            headers = {"Origin": base, "Content-Type": "application/json", "X-CSRF-Token": csrf}
            with opener.open(urllib.request.Request(base + path, body, headers), timeout=5) as response:
                raw = response.read().decode("utf-8")
                return json.loads(raw) if path.startswith("/admin/api/") else raw
        try:
            for _ in range(150):
                try:
                    homepage = request("/")
                    break
                except (OSError, urllib.error.URLError):
                    check(process.poll() is None, "Launcher exited before server startup")
                    time.sleep(.1)
            else:
                raise AssertionError("Package startup timed out")
            check('id="sidebar-stats-heading"' in homepage, "Default statistics component missing")
            credentials = (data / "admin-access.txt").read_text(encoding="utf-8").splitlines()
            login = {"username": credentials[0].split("：", 1)[1], "password": credentials[1].split("：", 1)[1]}
            credential_digest = hashlib.sha256((data / "runtime.env").read_bytes()).digest()
            session = request("/admin/api/session")
            session = request("/admin/api/login", login, session["csrf"])
            check(session.get("authenticated") is True, "Generated administrator could not sign in")
            settings = request("/admin/api/settings")
            if attempt == 0:
                check(settings["public_virtual_views_enabled"] is False and settings["base_url"] == "https://localhost", "Fresh install did not use real counters and its own URL")
                settings.update(title="Package smoke", public_virtual_views_enabled=True, public_stats_enabled=True, public_daily_views=2500)
                settings = request("/admin/api/settings", settings, session["csrf"])
                virtual_home = request("/")
                check(virtual_home != homepage, "Enabled virtual display did not change output")
                settings.update(public_virtual_views_enabled=False, public_stats_enabled=False)
                request("/admin/api/settings", settings, session["csrf"])
                check('id="sidebar-stats-heading"' not in request("/"), "Hidden statistics remained visible")
                settings.update(public_stats_enabled=True)
                request("/admin/api/settings", settings, session["csrf"])
                real_home = request("/")
                check('<bdi>0</bdi>' in real_home and 'id="sidebar-stats-heading"' in real_home, "Disabling did not return to recorded counters")
                before_credentials = credential_digest
                checks += ["fresh launcher startup", "generated administrator login", "real defaults", "toggle save and hide", "virtual display and real display"]
            else:
                check(credential_digest == before_credentials, "Restart replaced administrator credentials")
                check(settings["title"] == "Package smoke" and settings["public_virtual_views_enabled"] is False and settings["public_daily_views"] == 2500, "Settings did not persist across restart")
                checks += ["restart preserved credentials and settings"]
        finally:
            subprocess.run(["taskkill.exe", "/PID", str(process.pid), "/T", "/F"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            process.wait(timeout=15)
            log.close()
    backup = output / "backup" / "blog-database.json"
    subprocess.run([str(root / "ganwen.exe"), "database-export", "--data", str(data), "--file", str(backup)], env=environment, check=True, capture_output=True)
    check(backup.is_file(), "Logical database backup missing")
    target = output / "restored-data"
    subprocess.run([str(root / "ganwen.exe"), "database-import", "--data", str(target), "--file", str(backup)], env=environment, check=True, capture_output=True)
    check((target / "blog.db").is_file(), "Backup restore did not create database")
    with sqlite3.connect(target / "blog.db") as database:
        restored = json.loads(database.execute("SELECT value FROM kv WHERE key='site'").fetchone()[0])
    check(restored["title"] == "Package smoke" and restored["public_virtual_views_enabled"] is False and restored["public_daily_views"] == 2500, "Backup did not restore settings")
    checks += ["logical backup and empty-target restore"]
    result = {"version": version, "platform": "windows-amd64", "checks": checks, "passed": True}
    (output / "smoke-result.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
