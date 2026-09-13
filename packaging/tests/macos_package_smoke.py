#!/usr/bin/env python3
"""Install a macOS Headroom package and prove the desktop GUI renders.

`--archive` uses the manager spaces-in-path contract. `--from-home` screenshots
a user-layout install created by the public install.sh. Capture mode writes an
isolated config and never polls providers. Do not add banked-reset coverage.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import secrets
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from macos_finder_smoke import capture_finder_launch
from macos_gui_evidence import assert_rendered_png, write_evidence


def run(*arguments, timeout=90, **kwargs):
    return subprocess.run([str(argument) for argument in arguments], check=True, timeout=timeout, **kwargs)


def isolated_environment():
    return {key: value for key, value in os.environ.items()
            if not key.startswith(("QT_", "QML", "HEADROOM_", "DYLD_"))}


def clear_quarantine(*roots: Path):
    for root in roots:
        if not root.exists():
            continue
        subprocess.run(["xattr", "-d", "-r", "com.apple.quarantine", str(root)],
                       check=False, timeout=30, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def generation_app(install_root: Path) -> Path:
    state = json.loads((install_root / "install-state.json").read_text())
    return install_root / state["version_path"] / "Headroom.app"


def screenshot_generation(executable: Path, app: Path, image: Path, qpa: str, version: str) -> dict:
    ready = image.with_suffix(".ready.json")
    nonce = secrets.token_hex(24)
    env = isolated_environment() | {"QT_QPA_PLATFORM": qpa, "HEADROOM_READY_NONCE": nonce}
    if qpa == "offscreen":
        env["QT_QUICK_BACKEND"] = "software"
    run(executable, "--config", image.with_suffix(".settings.json"), "--headroom-ready-file", ready,
        "--screenshot", image, env=env, timeout=120)
    header = assert_rendered_png(image)
    value = json.loads(ready.read_text())
    assert value["nonce"] == nonce and value["version"] == version and value["pid"] > 0
    assert Path(value["executable"]).resolve() == (app / "Contents/MacOS/headroom").resolve()
    header.update(platform=qpa, nonce=nonce, pid=value["pid"], executable=value["executable"])
    return header


def install_with_manager(manager: Path, archive: Path, work: Path, version: str, architecture: str):
    install = work / "install with spaces"
    stable = work / "entries with spaces/headroom"
    run(manager, "install", "--archive", archive, "--install-root", install,
        "--entry-path", stable, "--version", version, "--platform", "macos", "--arch", architecture,
        timeout=180)
    app = generation_app(install)
    clear_quarantine(install, app)
    run("codesign", "--verify", "--deep", "--strict", app)
    return install, stable, app


def extract_manager(archive: Path, destination: Path) -> Path:
    import tarfile
    stem = archive.name.removesuffix(".tar.gz")
    with tarfile.open(archive, "r:gz") as handle:
        entry = handle.getmember(stem + "/bootstrap/headroom-package")
        assert entry.isfile() and 0 < entry.size <= 67108864
        destination.write_bytes(handle.extractfile(entry).read(entry.size + 1))
        assert destination.stat().st_size == entry.size
    destination.chmod(0o700)
    return destination


def screenshot_installed(work: Path, version: str, launcher: Path,
                         app: Path, prefix: str, finder_app: Path | None = None) -> list[dict]:
    captures = []
    for qpa in ("cocoa", "offscreen"):
        image = work / f"{prefix}-{qpa}.png"
        captures.append(screenshot_generation(launcher, app, image, qpa, version))
    if finder_app is not None:
        image = work / f"{prefix}-finder.png"
        capture_finder_launch(finder_app, image)
        captures.append(assert_rendered_png(image) | {"platform": "cocoa-finder"})
    inventory = work / ("macos-package-inventory.txt" if prefix == "headroom-macos" else f"{prefix}-inventory.txt")
    inventory.write_text(
        "\n".join(sorted(str(path.relative_to(app.parent)) for path in app.parent.rglob("*") if path.is_file())) + "\n")
    return captures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--version")
    parser.add_argument("--arch", choices=("x86_64", "arm64"), required=True)
    parser.add_argument("--from-home", type=Path, help="Screenshot a user-layout install already created by install.sh")
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("requires a native macOS runner")
    if bool(args.archive) == bool(args.from_home):
        parser.error("supply exactly one of --archive or --from-home")
    args.work.mkdir(parents=True, exist_ok=True)
    evidence = {
        "product": "Headroom",
        "architecture": args.arch,
        "machine": platform.machine(),
        "macos": platform.mac_ver()[0],
        "install_methods": [],
        "screenshots": [],
        "banked_reset_exercised": False,
        "notes": ("Screenshot mode uses an isolated --config file and never polls providers, "
                  "clicks banked reset, or calls ConsumeResetCredit/CodexReset."),
    }

    if args.from_home:
        home = args.from_home.resolve()
        install = home / "Library/Application Support/Headroom"
        finder = home / "Applications/Headroom.app"
        app = generation_app(install)
        state = json.loads((install / "install-state.json").read_text())
        version = state["active_version"]
        if args.version and args.version != version:
            parser.error(f"--version {args.version} does not match installed {version}")
        evidence["version"] = version
        evidence["install_methods"].append(os.environ.get("HEADROOM_SMOKE_INSTALL_SOURCE", "install.sh-existing"))
        clear_quarantine(install, finder, app)
        run("codesign", "--verify", "--deep", "--strict", app)
        evidence["screenshots"].extend(screenshot_installed(
            args.work, version, finder / "Contents/MacOS/headroom", app,
            "headroom-macos-user", finder_app=finder))
        run(sys.executable, Path(__file__).resolve().parent / "package_server_smoke.py",
            "--server", app / "Contents/MacOS/usage-server", timeout=180)
    else:
        version = args.version
        if not version:
            parser.error("--version is required with --archive")
        evidence["version"] = version
        manager = extract_manager(args.archive, args.work / "headroom-package")
        install, stable, app = install_with_manager(manager, args.archive, args.work, version, args.arch)
        evidence["install_methods"].append("headroom-package")
        evidence["screenshots"].extend(screenshot_installed(
            args.work, version, stable, app, "headroom-macos"))
        run(sys.executable, Path(__file__).resolve().parent / "package_server_smoke.py",
            "--server", app / "Contents/MacOS/usage-server", timeout=180)

    write_evidence(args.work / "macos-gui-evidence.json", evidence)
    print(json.dumps({key: evidence[key] for key in (
        "version", "architecture", "install_methods", "banked_reset_exercised")}, indent=2))


if __name__ == "__main__":
    main()
