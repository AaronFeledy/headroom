#!/usr/bin/env python3
"""Informational Qt key-import probe, isolated from the runner's login keychain.

Unlike macos_tls_fixture_ci.py, this starts with an EMPTY default/search keychain:
no pre-imported identities and no QT_SSL_USE_TEMPORARY_KEYCHAIN workaround.
Restricted to disposable hosted runners; restores keychain selection on exit.
"""
import os
from pathlib import Path
import secrets
import shlex
import signal
import subprocess
import sys
import tempfile


def security(*arguments: str) -> str:
    try:
        return subprocess.check_output(["/usr/bin/security", *arguments], text=True,
                                       stderr=subprocess.PIPE, timeout=30).strip()
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        raise SystemExit(f"Keychain operation {arguments[0]} failed; arguments withheld.") from None


def main() -> int:
    if (sys.platform != "darwin" or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise SystemExit("Empty-keychain probe is restricted to hosted macOS CI.")
    build = Path(sys.argv[1]).resolve(strict=True)
    log = Path(os.environ["RUNNER_TEMP"]) / "mac-tls-empty-keychain.log"
    default = shlex.split(security("default-keychain", "-d", "user"))
    search = shlex.split(security("list-keychains", "-d", "user"))
    if len(default) != 1:
        raise SystemExit("Cannot preserve the runner's default keychain.")
    env = dict(os.environ, QT_QPA_PLATFORM="offscreen", HEADROOM_TLS_FIXTURE_EMPTY_KEYCHAIN_PROBE="1")
    env.pop("QT_SSL_USE_TEMPORARY_KEYCHAIN", None)
    result = 1
    try:
        with tempfile.TemporaryDirectory(prefix="headroom-empty-keychain-") as temporary:
            keychain = str(Path(temporary) / "probe.keychain-db")
            password = secrets.token_hex(24)
            try:
                security("create-keychain", "-p", password, keychain)
                security("set-keychain-settings", "-lut", "900", keychain)
                security("unlock-keychain", "-p", password, keychain)
                security("default-keychain", "-d", "user", "-s", keychain)
                security("list-keychains", "-d", "user", "-s", keychain)
                with log.open("w", encoding="utf-8") as output:
                    failures = []
                    for binary, slot in (
                        ("headroom-appinfo-tests", "pinnedSessionRejectsReplacementBeforeSendingToken"),
                        ("headroom-login-tests", "proofVerification"),
                        ("headroom-managedserver-tests", "spawnsWithPrivateEnvironmentAndStopsOwned"),
                    ):
                        output.write(f"\n=== {binary}: {slot} ===\n")
                        output.flush()
                        with subprocess.Popen([str(build / binary), slot], env=env, start_new_session=True,
                                              stdout=output, stderr=subprocess.STDOUT) as process:
                            try:
                                failures.append(process.wait(timeout=180) != 0)
                            finally:
                                # A hung fixture child must die before the login keychain is restored.
                                try:
                                    os.killpg(process.pid, signal.SIGKILL)
                                except ProcessLookupError:
                                    output.write("Fixture process group already exited.\n")
                    result = int(any(failures))
            finally:
                try:
                    security("default-keychain", "-d", "user", "-s", *default)
                finally:
                    try:
                        security("list-keychains", "-d", "user", "-s", *search)
                    finally:
                        if Path(keychain).exists():
                            security("delete-keychain", keychain)
    finally:
        tail = "\n".join(log.read_text(encoding="utf-8").splitlines()[-80:]) if log.exists() else "Probe setup failed before tests."
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a", encoding="utf-8") as summary:
            summary.write(f"### Qt {os.environ['HEADROOM_QT_VERSION']} empty-keychain TLS probe: {'PASS' if result == 0 else 'FAIL'}\n\n"
                          f"No fixture pre-import or Qt temporary-keychain workaround; login keychain excluded.\n\n```text\n{tail}\n```\n")
        if not log.exists():
            log.write_text(tail + "\n", encoding="utf-8")
    return result


if __name__ == "__main__":
    sys.exit(main())
