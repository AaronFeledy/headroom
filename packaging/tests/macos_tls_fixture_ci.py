#!/usr/bin/env python3
"""Run native Qt TLS fixtures with disposable key storage on hosted macOS CI."""

import os
from pathlib import Path
import secrets
import shlex
import subprocess
import sys
import tempfile


def security(*arguments):
    return subprocess.check_output(["/usr/bin/security", *map(str, arguments)], text=True, timeout=30).strip()


def main():
    if (sys.platform != "darwin" or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise SystemExit("Disposable TLS keychain setup is restricted to hosted macOS CI.")
    if len(sys.argv) < 3:
        raise SystemExit("Supply the generated fixture directory and non-reset test command.")
    fixture = Path(sys.argv[1]).resolve(strict=True)
    for prefix in ("primary", "replacement"):
        for kind in ("certificate", "private-key"):
            path = fixture / f"{prefix}-{kind}.pem"
            if not path.is_file() or path.is_symlink() or not 0 < path.stat().st_size <= 16384:
                raise SystemExit("Generated TLS fixture PEM is missing or invalid.")
    previous_default = shlex.split(security("default-keychain", "-d", "user"))
    previous_search = shlex.split(security("list-keychains", "-d", "user"))
    if len(previous_default) != 1:
        raise SystemExit("Cannot preserve the runner's default keychain.")
    with tempfile.TemporaryDirectory(prefix="headroom-tls-fixtures-") as temporary:
        root = Path(temporary)
        keychain = root / "fixtures.keychain-db"
        password = secrets.token_hex(24)
        try:
            security("create-keychain", "-p", password, keychain)
            security("set-keychain-settings", "-lut", "1800", keychain)
            security("unlock-keychain", "-p", password, keychain)
            # Re-validate native TLS key import on each Qt kit/SDK upgrade;
            # keep the disposable-keychain fixture for native runner coverage.
            # Only public synthetic fixture keys are imported here. They are
            # not trusted roots, and peer verification/pinning stay enabled.
            for prefix in ("primary", "replacement"):
                certificate = fixture / f"{prefix}-certificate.pem"
                private_key = fixture / f"{prefix}-private-key.pem"
                archive = root / "identity.p12"
                subprocess.run(["/usr/bin/openssl", "pkcs12", "-export", "-in", str(certificate),
                                "-inkey", str(private_key), "-out", str(archive), "-passout", "pass:fixture"],
                               check=True, timeout=30, stdout=subprocess.DEVNULL)
                security("import", archive, "-k", keychain, "-P", "fixture", "-A")
            security("default-keychain", "-d", "user", "-s", keychain)
            security("list-keychains", "-d", "user", "-s", keychain, *previous_search)
            result = subprocess.run(sys.argv[2:], timeout=850)
            return result.returncode
        finally:
            security("default-keychain", "-d", "user", "-s", *previous_default)
            security("list-keychains", "-d", "user", "-s", *previous_search)
            if keychain.exists():
                security("delete-keychain", keychain)


if __name__ == "__main__":
    sys.exit(main())
