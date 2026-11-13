#!/usr/bin/env python3
"""Opt-in Linux integration test using an unused per-user headroom.service.

Requires a working current-user systemd bus. Refuses any existing unit, installs
only a temporary runtime link, and removes it on exit. All providers are disabled.
Never invoke a reset button, endpoint, or code that can consume a banked reset.
"""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager", type=Path, required=True)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    env = {key: value for key, value in os.environ.items() if not key.startswith(("HEADROOM_", "USAGE_"))}
    runtime_directory = Path("/run/user") / str(os.getuid())
    env.update(XDG_RUNTIME_DIR=str(runtime_directory), DBUS_SESSION_BUS_ADDRESS="unix:path=" + str(runtime_directory / "bus"))

    def systemctl(*arguments, check=True):
        return subprocess.run(["/usr/bin/systemctl", "--user", *arguments], env=env,
            check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)

    status = systemctl("show", "headroom.service", "--property=LoadState", "--property=ActiveState", "--property=FragmentPath").stdout
    if set(status.splitlines()) != {"LoadState=not-found", "ActiveState=inactive", "FragmentPath="}:
        raise SystemExit("Refusing to reserve an existing headroom.service unit.")
    runtime_link = runtime_directory / "systemd/user/headroom.service"
    if runtime_link.exists() or runtime_link.is_symlink():
        raise SystemExit("Refusing an existing runtime unit link.")

    with tempfile.TemporaryDirectory(prefix="headroom systemd smoke ") as temporary:
        directory = Path(temporary)
        root, entry = directory / "install", directory / "bin/headroom"
        def manager(command, *arguments, executable=None):
            completed = subprocess.run([str(executable or args.manager.resolve()), command, *map(str, arguments)],
                env=env, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40)
            reply = json.loads(completed.stdout)
            assert reply["ok"] is True and reply["command"] == command
            return reply["result"]
        manager("install", "--archive", args.archive.resolve(), "--install-root", root,
            "--entry-path", entry, "--cli-entry-path", entry)
        state = json.loads((root / "install-state.json").read_text())
        runtime = root / state["version_path"] / "bin/headroom"
        immutable_manager = runtime.with_name("headroom-package")
        config = directory / "server.yaml"
        config.write_text("providers:\n" + "".join(f"  {name}:\n    enabled: false\n" for name in ("claude", "codex", "cursor", "grok", "opencodego")))
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        address = f"http://127.0.0.1:{port}"
        unit = directory / "headroom.service"
        def quote(value):
            return '"' + str(value).replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
        unit.write_text("[Unit]\nDescription=Disposable Headroom integration fixture\n[Service]\nType=exec\n"
            + f"WorkingDirectory={directory}\n"
            + "Environment=USAGE_PROVIDER_CLAUDE_ENABLED=false USAGE_PROVIDER_CODEX_ENABLED=false USAGE_PROVIDER_CURSOR_ENABLED=false USAGE_PROVIDER_GROK_ENABLED=false USAGE_PROVIDER_OPENCODEGO_ENABLED=false\n"
            + f"ExecStart={quote(entry)} serve --config {quote(config)} --listen-addr 127.0.0.1:{port} --auth-token \"\" --ssh-access=false\n"
            + "Restart=on-failure\nRestartSec=1\nTimeoutStopSec=10\nUMask=0077\n")
        watch = None
        try:
            systemctl("link", "--runtime", str(unit))
            systemctl("daemon-reload")
            systemctl("start", "headroom.service")
            def healthy():
                try:
                    with urllib.request.urlopen(address + "/api/v1/health", timeout=1) as response:
                        return response.status == 200
                except (urllib.error.URLError, TimeoutError):
                    return False
            deadline = time.monotonic() + 15
            while not healthy():
                if time.monotonic() > deadline:
                    raise AssertionError("provider-disabled user service did not become healthy")
                time.sleep(.1)
            before = json.loads((root / "runtime/managed-serve.json").read_text())
            assert before["ready"] and before["supervisor"] == "systemd-user"
            stage = manager("stage", "--archive", args.archive.resolve(), "--install-root", root)
            stage_record = Path(stage["package_root"]).parent.parent / "verified-stage.json"
            watch_env = env | {"HEADROOM_INSTALL_ROOT": str(root), "HEADROOM_PACKAGE_VERSION": args.version}
            watch = subprocess.Popen([str(runtime), "--watch", "--url", address], env=watch_env,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            prepared = manager("prepare-apply", "--install-root", root, "--entry-path", entry,
                "--stage-record", stage_record, "--current-pid", watch.pid, "--current-executable", runtime,
                "--current-role", "cli", "--candidate-role", "cli", executable=immutable_manager)
            commit = Path(prepared["commit_path"])
            temporary_commit = commit.with_suffix(".tmp")
            with temporary_commit.open("w") as output:
                os.chmod(temporary_commit, 0o600)
                json.dump({"schema": 1, "product": "Headroom", "commit": True, "nonce": prepared["nonce"]}, output)
                output.flush()
                os.fsync(output.fileno())
            temporary_commit.replace(commit)
            watch.terminate()
            watch.wait(timeout=10)
            deadline = time.monotonic() + 90
            while not (root / "last-apply-result.json").is_file():
                if time.monotonic() > deadline:
                    raise AssertionError("managed user-service repair did not finish")
                time.sleep(.1)
            result = json.loads((root / "last-apply-result.json").read_text())
            assert result["status"] == "applied", (result.get("status"), result.get("message"))
            after = json.loads((root / "runtime/managed-serve.json").read_text())
            state = json.loads((root / "install-state.json").read_text())
            assert after["ready"] and after["pid"] != before["pid"]
            assert after["process_token"] != before["process_token"]
            assert Path(after["executable"]) == root / state["version_path"] / "bin/headroom"
            assert healthy()
            print("Real per-user service install, exact-process repair, restart, and readiness passed.")
        finally:
            if watch is not None and watch.poll() is None:
                watch.terminate()
                watch.wait(timeout=10)
            if runtime_link.is_symlink() and runtime_link.resolve() == unit:
                systemctl("stop", "headroom.service", check=False)
                active = systemctl("show", "headroom.service", "--property=ActiveState", "--value").stdout.strip()
                if active not in ("inactive", "failed"):
                    raise AssertionError("fixture unit did not stop; refusing to remove its runtime link")
                runtime_link.unlink()
                systemctl("daemon-reload")
                systemctl("reset-failed", "headroom.service", check=False)


if __name__ == "__main__":
    main()
