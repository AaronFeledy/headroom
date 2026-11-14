#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Check Claude Code's embedded credential contract without executing its binary.

Run: python3 packaging/claude_code_contract.py [--binary /path/to/claude]
"""

import argparse
import hashlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path
from typing import BinaryIO, Final


PACKAGE: Final = "@anthropic-ai/claude-code-darwin-arm64"
CHUNK_SIZE: Final = 1024 * 1024
OVERLAP: Final = 8192
# Only what Headroom reads today: Keychain service and account naming, the read
# command, and the Keychain-then-file order. Match literals and bounded syntax,
# never the names assigned by the minifier.
MARKERS: Final = {
    "service credentials suffix": rb'"-credentials"',
    "service OAuth suffix template": rb'`Claude Code\$\{[^{}\n]{1,256}\.OAUTH_FILE_SUFFIX\}\$\{[^{}\n]{1,256}\}\$\{[^{}\n]{1,256}\}`',
    "service directory hash": rb'\.digest\("hex"\)\.substring\(0,8\)',
    "service directory NFC": rb'\.normalize\("NFC"\)',
    "account USER fallback": rb'process\.env\.USER\|\|[^;\n]{0,256}\.username',
    "account default": rb'"claude-code-user"',
    "account safe characters": rb'/\^\[a-zA-Z0-9\._-\]\+\$/',
    "keychain read argv": rb'\["find-generic-password","-a",[^,\n]{1,256},"-w","-s",',
    "plaintext credentials file": rb'"\.credentials\.json"',
    "keychain fallback composition": rb'`\$\{[^{}\n]{1,256}\.name\}-with-\$\{[^{}\n]{1,256}\.name\}-fallback`',
}


def missing_markers(binary: BinaryIO) -> tuple[str, ...]:
    """Scan once with bounded memory, retaining matches across chunk boundaries."""
    pending = {name: re.compile(pattern) for name, pattern in MARKERS.items()}
    tail = b""
    while chunk := binary.read(CHUNK_SIZE):
        window = tail + chunk
        for name, pattern in tuple(pending.items()):
            if pattern.search(window):
                del pending[name]
        if not pending:
            break
        tail = window[-OVERLAP:]
    return tuple(pending)


def check_binary(path: Path) -> int:
    """Print named contract failures and return a shell exit status."""
    with path.open("rb") as binary:
        missing = missing_markers(binary)
    if missing:
        print("Missing Claude Code contract markers:", file=sys.stderr)
        for name in missing:
            print(f"  - {name}", file=sys.stderr)
        return 1
    print(f"Claude Code credential contract: all {len(MARKERS)} markers found")
    return 0


def main() -> int:
    """Download just the native package, or scan a supplied binary offline."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="scan an existing Claude binary offline")
    args = parser.parse_args()
    if args.binary is not None:
        print(f"Claude Code local binary: {args.binary} (package version unavailable)")
        return check_binary(args.binary)
    with tempfile.TemporaryDirectory(prefix="claude-code-contract-") as directory:
        root = Path(directory)
        subprocess.run(
            ["npm", "pack", f"{PACKAGE}@latest", "--ignore-scripts", "--silent"],
            cwd=root, check=True, timeout=300,
        )
        archive, = root.glob("*.tgz")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        # The npm archive basename ends in the resolved package version.
        version = archive.name.removeprefix("anthropic-ai-claude-code-darwin-arm64-").removesuffix(".tgz")
        print(f"{PACKAGE} version: {version} (sha256 {digest})", flush=True)
        binary_path = root / "claude"
        with tarfile.open(archive, "r:gz") as package:
            member = package.getmember("package/claude")
            if not member.isfile():
                print("package/claude is not a regular file", file=sys.stderr)
                return 1
            source = package.extractfile(member)
            if source is None:
                print("package/claude has no contents", file=sys.stderr)
                return 1
            with source, binary_path.open("wb") as destination:
                shutil.copyfileobj(source, destination, length=CHUNK_SIZE)
        return check_binary(binary_path)


if __name__ == "__main__":
    sys.exit(main())
