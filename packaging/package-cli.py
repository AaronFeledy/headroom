#!/usr/bin/env python3
"""Assemble a Qt-free CLI archive using the existing Go package validator."""
import argparse
from pathlib import Path
import shutil
import subprocess
import sys
from validate_version import validate


def copy(source, destination, executable=False):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
    destination.chmod(0o755 if executable else 0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--platform", choices=("windows", "linux", "macos"), required=True)
    parser.add_argument("--arch", choices=("x86_64", "arm64"), required=True)
    for name in ("cli", "launcher", "manager", "validator", "work", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    validate(args.version)
    arch = "x64" if args.platform == "windows" and args.arch == "x86_64" else args.arch
    stem = f"Headroom-CLI-v{args.version}-{args.platform}-{arch}"
    root = args.work.resolve() / stem
    if root.exists():
        raise SystemExit("CLI package work directory already exists; use a fresh directory")
    extension = ".exe" if args.platform == "windows" else ""
    binaries = {
        "bootstrap/headroom": args.launcher,
        "bootstrap/headroom-package": args.manager,
        "bundle/bin/headroom": args.cli,
        "bundle/bin/headroom-package": args.manager,
    }
    for name, source in binaries.items():
        copy(source.resolve(), root / (name + extension), True)
    repository = Path(__file__).resolve().parent.parent
    copy(repository / "LICENSE", root / "bundle/share/licenses/headroom/LICENSE")
    notices = root / "bundle/share/headroom/THIRD_PARTY_NOTICES.txt"
    notices.parent.mkdir(parents=True)
    notices.write_text("Headroom CLI includes the Go runtime, golang.org/x/sys, golang.org/x/term, "
                       "Google Protocol Buffers for Go, and go-yaml v3.\n"
                       "Their license texts are included under share/licenses/go. No Qt runtime is included.\n")
    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
    go_version = subprocess.check_output(["go", "env", "GOVERSION"], text=True).strip()
    go_release = ".".join(go_version.removeprefix("go").split(".")[:2])
    cache = Path(subprocess.check_output(["go", "env", "GOMODCACHE"], text=True).strip())
    runtime_license = next((path for path in (goroot / "LICENSE", Path("/usr/share/licenses/go/LICENSE")) if path.is_file()), None)
    if runtime_license is None:
        raise SystemExit("the installed Go runtime license could not be found")
    copy(runtime_license, root / "bundle/share/licenses/go/runtime/LICENSE")
    # Module versions are resolved from the module graph, never silently from a
    # newer cache entry. The archive itself is validated only by the Go manager.
    modules = ("golang.org/x/sys", "golang.org/x/term", "google.golang.org/protobuf", "gopkg.in/yaml.v3")
    for module in modules:
        version = subprocess.check_output(["go", "list", "-m", "-f", "{{.Version}}", module],
            cwd=repository / "packaging/headroom-manager", text=True).strip()
        source = cache / f"{module}@{version}"
        target = root / "bundle/share/licenses/go" / module
        copy(source / "LICENSE", target / "LICENSE")
        if (source / "NOTICE").is_file():
            copy(source / "NOTICE", target / "NOTICE")
    if args.platform == "macos":
        if sys.platform != "darwin":
            raise SystemExit("macOS CLI packages require native signing and execution validation")
        for name in binaries:
            subprocess.run(["codesign", "--force", "--sign", "-", str(root / name)], check=True)
    args.output.mkdir(parents=True, exist_ok=True)
    archive = args.output.resolve() / (stem + (".zip" if args.platform == "windows" else ".tar.gz"))
    subprocess.run([str(args.validator.resolve()), "create-package", "--kind", "cli", "--root", str(root),
        "--output", str(archive), "--version", args.version, "--platform", args.platform, "--arch", args.arch,
        "--baseline", f"Go {go_release} native CLI"], check=True)


if __name__ == "__main__":
    main()
