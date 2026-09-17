#!/usr/bin/env python3
"""Launch the installed Finder entry with an isolated capture profile."""

import argparse
import os
import json
import plistlib
from pathlib import Path
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", type=Path, required=True)
    parser.add_argument("--image", type=Path, required=True)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("requires macOS Launch Services")
    contents = args.app / "Contents"
    info = plistlib.loads((contents / "Info.plist").read_bytes())
    if info.get("CFBundleIdentifier") != "io.headroom.launcher" or info.get("CFBundleIconFile") != "headroom.icns":
        raise AssertionError("Finder launcher lost its stable identity/icon")
    root = Path((contents / "MacOS/headroom.root").read_text().strip())
    state = json.loads((root / "install-state.json").read_text())
    payload = root / state["version_path"] / "Headroom.app/Contents"
    if (contents / "Resources/headroom.icns").read_bytes() != (payload / "Resources/headroom.icns").read_bytes():
        raise AssertionError("Finder launcher icon differs from the installed desktop")
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith(("QT_", "QML", "HEADROOM_", "DYLD_"))}
    # Capture mode cannot poll providers or acquire public updates. Opening the
    # outer app exercises Launch Services, the stable launcher, and .root lookup.
    subprocess.run(["/usr/bin/open", "-n", "-W", str(args.app), "--args", "--config",
                    str(args.image.with_suffix(".settings.json")), "--screenshot", str(args.image)],
                   env=environment, check=True, timeout=90)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if args.image.is_file() and args.image.stat().st_size > 0:
            return
        time.sleep(0.1)
    raise RuntimeError("Finder launcher did not produce the isolated capture")


if __name__ == "__main__":
    main()
