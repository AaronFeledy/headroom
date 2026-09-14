#!/usr/bin/env python3
"""Verify the downloaded macOS package in an isolated root without a Qt SDK."""

import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tarfile
import time


def run(*arguments, **kwargs):
    return subprocess.run([str(argument) for argument in arguments], check=True, timeout=90, **kwargs)


def wait_for_capture(image, ready, timeout=30):
    # Launch Services returns after starting the app, before its capture is
    # rendered. The PNG end marker avoids accepting a partially written file.
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            data = image.read_bytes()
            if (ready.is_file() and data.startswith(b"\x89PNG\r\n\x1a\n")
                    and data.endswith(b"\x00\x00\x00\x00IEND\xaeB\x60\x82")):
                return
        except FileNotFoundError:
            pass
        time.sleep(0.1)
    raise RuntimeError(f"Mac launcher did not complete capture and readiness: {image}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--arch", choices=("x86_64", "arm64"), required=True)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("requires a native macOS runner")
    args.work.mkdir(parents=True, exist_ok=True)
    stem = args.archive.name.removesuffix(".tar.gz")
    manager = args.work / "headroom-package"
    with tarfile.open(args.archive, "r:gz") as archive:
        entry = archive.getmember(stem + "/bootstrap/headroom-package")
        assert entry.isfile() and 0 < entry.size <= 67108864
        manager.write_bytes(archive.extractfile(entry).read(entry.size + 1))
        assert manager.stat().st_size == entry.size
    manager.chmod(0o700)
    install = args.work / "install with spaces"
    stable = args.work / "entries with spaces/headroom"
    run(manager, "install", "--archive", args.archive, "--install-root", install,
        "--entry-path", stable, "--version", args.version, "--platform", "macos", "--arch", args.arch)
    state = json.loads((install / "install-state.json").read_text())
    generation = install / state["version_path"]
    app = generation / "Headroom.app"
    run("codesign", "--verify", "--deep", "--strict", app)
    # Never load a live profile or poll a provider. Screenshot mode renders an
    # empty connection state, and the server smoke uses provider-disabled config.
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("QT_", "QML", "HEADROOM_", "DYLD_"))}
    for platform in ("cocoa", "offscreen"):
        image = args.work / f"headroom-macos-{platform}.png"
        ready = image.with_suffix(".ready.json")
        image.unlink(missing_ok=True)
        ready.unlink(missing_ok=True)
        nonce = secrets.token_hex(24)
        env = environment | {"QT_QPA_PLATFORM": platform, "HEADROOM_READY_NONCE": nonce}
        if platform == "offscreen":
            env["QT_QUICK_BACKEND"] = "software"
        run(stable, "--config", image.with_suffix(".settings.json"), "--headroom-ready-file", ready,
            "--screenshot", image, env=env)
        wait_for_capture(image, ready)
        assert image.stat().st_size > 0
        value = json.loads(ready.read_text())
        assert value["nonce"] == nonce and value["version"] == args.version and value["pid"] > 0
        assert Path(value["executable"]).resolve() == (app / "Contents/MacOS/headroom").resolve()
    run(sys.executable, "packaging/tests/package_server_smoke.py", "--server", app / "Contents/MacOS/usage-server")
    (args.work / "macos-package-inventory.txt").write_text(
        "\n".join(sorted(str(path.relative_to(generation)) for path in generation.rglob("*") if path.is_file())) + "\n")


if __name__ == "__main__":
    main()
