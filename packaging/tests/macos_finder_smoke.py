#!/usr/bin/env python3
"""Launch the installed Finder entry with an isolated capture profile.

Capture mode cannot poll providers or acquire public updates. Opening the
outer app exercises Launch Services, the stable launcher, and .root lookup.
Do not add banked-reset or ConsumeResetCredit coverage here.
"""

import argparse
import os
from pathlib import Path
import subprocess
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))


def isolated_environment():
    return {key: value for key, value in os.environ.items()
            if not key.startswith(("QT_", "QML", "HEADROOM_", "DYLD_"))}


def capture_finder_launch(app: Path, image: Path, timeout=90):
    environment = isolated_environment()
    subprocess.run(["/usr/bin/open", "-n", "-W", str(app), "--args", "--config",
                    str(image.with_suffix(".settings.json")), "--screenshot", str(image)],
                   env=environment, check=True, timeout=timeout)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if image.is_file() and image.stat().st_size > 0:
            return image
        time.sleep(0.1)
    raise RuntimeError("Finder launcher did not produce the isolated capture")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", type=Path, required=True)
    parser.add_argument("--image", type=Path, required=True)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("requires macOS Launch Services")
    capture_finder_launch(args.app, args.image)
    from macos_gui_evidence import assert_rendered_png
    assert_rendered_png(args.image)


if __name__ == "__main__":
    main()
