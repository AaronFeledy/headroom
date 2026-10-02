#!/usr/bin/env python3
"""Check native Mach-O slices and deployment targets before signing a Mac package."""

import argparse
from pathlib import Path
import re
import subprocess


MACHO_MAGIC = {b"\xfe\xed\xfa\xce", b"\xce\xfa\xed\xfe", b"\xfe\xed\xfa\xcf", b"\xcf\xfa\xed\xfe",
               b"\xca\xfe\xba\xbe", b"\xbe\xba\xfe\xca", b"\xca\xfe\xba\xbf", b"\xbf\xba\xfe\xca"}


def check(path, architecture):
    slices = subprocess.check_output(["lipo", "-archs", str(path)], text=True).split()
    if architecture not in slices:
        raise ValueError(f"{path.name}: missing {architecture} Mach-O slice")
    headers = subprocess.check_output(["otool", "-arch", architecture, "-l", str(path)], text=True)
    targets = []
    for block in headers.split("Load command"):
        if re.search(r"^\s*cmd LC_BUILD_VERSION\s*$", block, re.MULTILINE):
            if not re.search(r"^\s*platform (?:1|MACOS)\s*$", block, re.MULTILINE):
                raise ValueError(f"{path.name}: {architecture} slice is not a macOS binary")
            # LC_BUILD_VERSION also includes the linker's `version`; only
            # `minos` describes the operating-system deployment target.
            field = "minos"
        elif re.search(r"^\s*cmd LC_VERSION_MIN_MACOSX\s*$", block, re.MULTILINE):
            field = "version"
        else:
            continue
        targets.extend(re.findall(rf"^\s*{field}\s+(\d+)\.(\d+)(?:\.(\d+))?\s*$", block, re.MULTILINE))
    if len(targets) != 1 or tuple(int(part or 0) for part in targets[0]) > (14, 4, 0):
        raise ValueError(f"{path.name}: {architecture} slice does not support the macOS 14.4 baseline")


def check_tree(root, architecture):
    count = 0
    paths = (root,) if root.is_file() else root.rglob("*")
    for path in paths:
        if path.is_symlink() or not path.is_file():
            continue
        with path.open("rb") as source:
            magic = source.read(4)
        if magic in MACHO_MAGIC:
            check(path, architecture)
            count += 1
    if not count:
        raise ValueError("no Mach-O payloads were found")
    return count


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", type=Path)
    parser.add_argument("architecture", choices=("arm64", "x86_64"))
    args = parser.parse_args()
    print(f"Verified {check_tree(args.path, args.architecture)} Mach-O payloads for {args.architecture} and macOS 14.4")
