#!/usr/bin/env python3
"""Assemble the native macOS payload; archive validation belongs to the Go manager."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


def run(*arguments, **kwargs):
    subprocess.run([str(argument) for argument in arguments], check=True, **kwargs)


def copy_file(source, target, executable=False):
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    target.chmod(0o755 if executable else 0o644)


def canonical_app_bundle(bundle):
    candidates = [path for path in bundle.iterdir() if path.name.lower() == "headroom.app"]
    if len(candidates) != 1 or not candidates[0].is_dir() or candidates[0].is_symlink():
        raise ValueError("CMake did not install one Headroom app bundle")
    app = bundle / "Headroom.app"
    if candidates[0].name != app.name:
        # exists() cannot distinguish these spellings on the default Mac
        # filesystem. Rename through a distinct name to preserve canonical case.
        temporary = bundle / ".headroom-app-rename"
        candidates[0].rename(temporary)
        temporary.rename(app)
    return app


def relocate_qml_plugins(app):
    # Qt deploys QML plugins centrally with @loader_path dependencies relative
    # to Contents/PlugIns. Keep them there: copying them beside each qmldir
    # breaks those load commands. qmldir's documented plugin-path argument
    # locates the central binary without links outside the framework contract.
    plugin_directory = app / "Contents/PlugIns"
    qml = app / "Contents/Resources/qml"
    if plugin_directory.is_symlink() or not plugin_directory.is_dir():
        raise ValueError("QML plugin directory is missing or unsafe")
    if qml.is_symlink() or not qml.is_dir():
        raise ValueError("QML import directory is missing or unsafe")
    plugins = plugin_directory.resolve(strict=True)
    plugin_line = re.compile(r"^(optional )?plugin ([A-Za-z0-9_]+)$")
    declared_entries = set()

    for qmldir in sorted(qml.rglob("qmldir")):
        if qmldir.is_symlink() or not qmldir.is_file():
            raise ValueError("unexpected QML module definition: " + str(qmldir.relative_to(app)))
        lines = qmldir.read_text().splitlines()
        changed = False
        for index, line in enumerate(lines):
            if not line.startswith("plugin ") and not line.startswith("optional plugin "):
                continue
            match = plugin_line.fullmatch(line)
            if not match:
                raise ValueError("unexpected QML plugin directive: " + str(qmldir.relative_to(app)))
            name = match.group(2)
            local = qmldir.parent / f"lib{name}.dylib"
            if local in declared_entries:
                raise ValueError("duplicate QML plugin declaration: " + str(local.relative_to(app)))
            declared_entries.add(local)
            central = plugins / local.name
            if central.is_symlink() or not central.is_file():
                raise ValueError("expected deployed QML plugin is missing: " + str(central.relative_to(app)))
            if local.is_symlink():
                if local.resolve(strict=True) != central:
                    raise ValueError("unexpected QML deployment link: " + str(local.relative_to(app)))
            elif local.exists() and not local.is_file():
                raise ValueError("unexpected QML module plugin: " + str(local.relative_to(app)))

            relative_plugins = Path(os.path.relpath(plugins, qmldir.parent)).as_posix()
            prefix = "optional " if match.group(1) else ""
            lines[index] = f"{prefix}plugin {name} {relative_plugins}"
            # Qt 6.12's CMake deployment removes the build-tree copy/link
            # beside qmldir. Older deployment layouts can still leave one.
            # In either case the exact central binary checked above is required.
            local.unlink(missing_ok=True)
            changed = True
        if changed:
            qmldir.write_text("\n".join(lines) + "\n")

    for path in qml.rglob("*"):
        if path.is_symlink():
            raise ValueError("unexpected QML deployment link: " + str(path.relative_to(app)))
        if path.suffix == ".dylib" and path not in declared_entries:
            raise ValueError("undeclared QML plugin: " + str(path.relative_to(app)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("architecture", choices=("x86_64", "arm64"))
    for name in ("build_dir", "work_dir", "output_dir", "server", "launcher", "manager", "qt_root", "qt_source_cache"):
        parser.add_argument(name, type=Path)
    parser.add_argument("cli", type=Path)
    parser.add_argument("cli_launcher", type=Path)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("macOS package assembly requires a native Mac")
    for name in ("build_dir", "work_dir", "output_dir", "server", "launcher", "manager", "qt_root", "qt_source_cache", "cli", "cli_launcher"):
        setattr(args, name, getattr(args, name).resolve())
    run(args.manager, "asset-name", "--version", args.version, "--platform", "macos", "--arch", args.architecture)
    stem = f"Headroom-v{args.version}-macos-{args.architecture}"
    root = args.work_dir / stem
    if root.exists():
        shutil.rmtree(root)
    bundle = root / "bundle"
    bundle.mkdir(parents=True)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    run("cmake", "--install", args.build_dir, "--prefix", bundle)
    app = canonical_app_bundle(bundle)
    if not (app / "Contents/MacOS/headroom").is_file():
        raise ValueError("CMake did not install the expected Headroom app bundle")
    metadata = json.loads((args.build_dir / "headroom-build-metadata.json").read_text())
    if metadata.get("product") != "Headroom" or metadata.get("version") != args.version:
        raise ValueError("desktop build version does not match the package")

    copy_file(args.server, app / "Contents/MacOS/usage-server", True)
    copy_file(args.launcher, root / "bootstrap/headroom", True)
    copy_file(args.manager, root / "bootstrap/headroom-package", True)
    copy_file(args.manager, bundle / "bin/headroom-package", True)
    copy_file(args.cli, app / "Contents/MacOS/headroom-cli", True)
    copy_file(args.cli_launcher, root / "bootstrap/headroom-cli", True)
    # Deploy with Qt's tool, then explicitly include offline smoke and native
    # TLS plugins. A second pass fixes their framework references as well.
    plugins = ("platforms/libqcocoa.dylib", "platforms/libqoffscreen.dylib",
               "tls/libqsecuretransportbackend.dylib", "imageformats/libqsvg.dylib",
               "iconengines/libqsvgicon.dylib")
    for relative in plugins:
        copy_file(args.qt_root / "plugins" / relative, app / "Contents/PlugIns" / relative, True)
    run(args.qt_root / "bin/macdeployqt", app,
        "-qmldir=" + str(Path("clients/desktop/qml").resolve()), "-always-overwrite")
    # Use macOS's native TLS implementation consistently, including machines
    # that happen to have Homebrew OpenSSL installed.
    (app / "Contents/PlugIns/tls/libqopensslbackend.dylib").unlink(missing_ok=True)
    for relative in plugins:
        if not (app / "Contents/PlugIns" / relative).is_file():
            raise ValueError("required deployed plugin is missing: " + relative)
    relocate_qml_plugins(app)

    share = bundle / "share"
    copy_file(Path("packaging/THIRD_PARTY_NOTICES.txt"), share / "headroom/THIRD_PARTY_NOTICES.txt")
    copy_file(Path("LICENSE"), share / "licenses/headroom/LICENSE")
    for source in Path("packaging/licenses/qt").glob("*.txt"):
        copy_file(source, share / "licenses/qt" / source.name)
    for directory in (args.qt_root / "LICENSES", args.qt_root / "licenses"):
        if directory.is_dir():
            for source in directory.rglob("*"):
                if source.is_file():
                    copy_file(source, share / "licenses/qt/kit" / source.relative_to(directory))
    run(sys.executable, "packaging/qt_attributions.py", "package", "--source-cache", args.qt_source_cache,
        "--qt-root", args.qt_root, "--payload-root", bundle,
        "--output", share / "licenses/qt/attributions",
        "--modules", "qtbase", "qtdeclarative", "qtshadertools", "qtsvg")
    attribution = json.loads((share / "licenses/qt/attributions/index.json").read_text())
    if attribution.get("qt_version") != metadata["qt_version"]:
        raise ValueError("Qt attribution version does not match the desktop")
    go = json.loads(subprocess.check_output(["go", "env", "-json", "GOROOT", "GOMODCACHE"], text=True))
    copy_file(Path(go["GOROOT"]) / "LICENSE", share / "licenses/go/runtime/LICENSE")
    module_cache = Path(go["GOMODCACHE"])
    for module, destination, names in (
        ("google.golang.org/protobuf@v1.36.11", "protobuf", ("LICENSE",)),
        ("gopkg.in/yaml.v3@v3.0.1", "yaml", ("LICENSE", "NOTICE")),
    ):
        for name in names:
            copy_file(module_cache / module / name, share / "licenses/go" / destination / name)
    # The manager's Darwin process identity uses x/sys. Derive its pinned
    # module location rather than guessing a version here.
    for module in ("sys", "term"):
        info = json.loads(subprocess.check_output(
            ["go", "list", "-m", "-json", "golang.org/x/" + module], cwd="packaging/headroom-manager", text=True))
        copy_file(Path(info["Dir"]) / "LICENSE", share / f"licenses/go/x-{module}/LICENSE")

    # Keep native framework symlinks intact. They are part of Apple's signing
    # format and are checked by the manager's scoped macOS link contract.
    # Ad-hoc signatures make architecture/code integrity testable without a
    # Developer ID. This is not Developer ID signing or Apple notarization.
    run(sys.executable, "packaging/check_macos_runtime.py", root, args.architecture)
    for executable in (root / "bootstrap/headroom", root / "bootstrap/headroom-package",
                       root / "bootstrap/headroom-cli", bundle / "bin/headroom-package",
                       app / "Contents/MacOS/usage-server", app / "Contents/MacOS/headroom-cli"):
        run("codesign", "--force", "--sign", "-", executable)
    # Retain the exact signed public router for first-upgrade migration by an
    # older manager, which only copies the generation's bundle subtree.
    copy_file(root / "bootstrap/headroom-cli", bundle / "bin/headroom-cli-launcher", True)
    run("codesign", "--force", "--deep", "--sign", "-", app)
    run("codesign", "--verify", "--deep", "--strict", app)
    archive = args.output_dir / (stem + ".tar.gz")
    run(args.manager, "create-package", "--root", root, "--output", archive,
        "--version", args.version, "--platform", "macos", "--arch", args.architecture,
        "--qt-version", metadata["qt_version"], "--baseline", "macos-14.4")
    run(args.manager, "verify", "--archive", archive, "--version", args.version,
        "--platform", "macos", "--arch", args.architecture, "--asset", archive.name)


if __name__ == "__main__":
    main()
