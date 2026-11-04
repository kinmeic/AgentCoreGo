#!/usr/bin/env python3
"""Smoke-test a released executable, then package it with its configuration."""
import argparse
import json
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import zipfile


def smoke(binary, tag, emulator, root):
    command = [*emulator, str(binary)]
    version = subprocess.check_output([*command, "version"], text=True, timeout=15)
    if version.strip() != tag.removeprefix("v"):
        raise RuntimeError(f"unexpected executable version: {version.strip()}")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    config = root / "smoke.json"
    db = root / "data" / "agent.db"
    config.write_text(json.dumps({
        "server": {"port": port, "terminalEnabled": False},
        "storage": {"dir": str(db.parent), "sessionDbPath": str(db)},
        "workspace": {"path": str(root / "workspace")},
        "llm": {"model": "release-smoke"},
    }), encoding="utf-8")
    log = root / "smoke.log"
    with log.open("wb") as output:
        process = subprocess.Popen([*command, "-config", str(config)], cwd=root,
                                   stdout=output, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 20
            while process.poll() is None and time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{port}/ready", timeout=1) as response:
                        readiness = json.load(response)
                        if readiness["status"] == "ready" and db.stat().st_size > 0:
                            return
                except (OSError, urllib.error.URLError):
                    time.sleep(0.2)
            raise RuntimeError(f"release binary failed readiness:\n{log.read_text(errors='replace')}")
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("tag")
    parser.add_argument("platform")
    parser.add_argument("--emulator", nargs=argparse.REMAINDER, default=[])
    args = parser.parse_args()
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.tag):
        parser.error("expected a vMAJOR.MINOR.PATCH tag")
    if not re.fullmatch(r"[a-z0-9_-]+", args.platform):
        parser.error("invalid platform name")
    repository = Path(__file__).resolve().parent.parent
    output = repository / "dist"
    output.mkdir(exist_ok=True)
    name = f"agent-core-{args.tag}-{args.platform}"
    windows = args.platform.startswith("windows-")
    with tempfile.TemporaryDirectory(prefix="agent-core-release-") as temp:
        root = Path(temp)
        smoke(args.binary.resolve(), args.tag, args.emulator, root)
        package = root / name
        package.mkdir()
        binary = package / ("agent-core.exe" if windows else "agent-core")
        shutil.copyfile(args.binary, binary)
        binary.chmod(0o755)
        for filename in ("README.md", "LICENSE", "agent-core.yaml"):
            shutil.copyfile(repository / filename, package / filename)
        shutil.copyfile(repository / "docs" / "security.md", package / "SECURITY.md")
        if windows:
            archive = output / f"{name}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as file:
                for entry in sorted(package.iterdir()):
                    file.write(entry, entry.relative_to(root))
        else:
            archive = output / f"{name}.tar.gz"
            with tarfile.open(archive, "w:gz") as file:
                file.add(package, arcname=name)
    print(archive)


if __name__ == "__main__":
    main()
