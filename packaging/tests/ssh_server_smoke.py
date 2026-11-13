#!/usr/bin/env python3
"""Exercise an actual OpenSSH connection to an isolated Headroom server.

Linux only. Requires ssh, sshd, ssh-keygen and a usable current-user SSH/PAM
account. Nothing is written to the user's SSH configuration or provider files.
The test reserves the account's fixed SSH socket and refuses to run if it exists.
"""
import argparse
import base64
import getpass
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time


def unused_port():
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        return reservation.getsockname()[1]


def stop(process):
    if process is None:
        return
    process.terminate()
    try:
        process.wait(5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(5)


def run(server_path, qt_probe=None):
    ssh, sshd, keygen = (shutil.which(name) for name in ("ssh", "sshd", "ssh-keygen"))
    if sys.platform != "linux" or not all((ssh, sshd, keygen)):
        raise RuntimeError("This smoke test requires Linux and OpenSSH client/server tools")
    import pwd
    server_path = str(Path(server_path).resolve(strict=True))
    account_home = Path(pwd.getpwuid(os.geteuid()).pw_dir)
    control = account_home / ".local/share/headroom/ssh/control.sock"
    if os.path.lexists(control):
        raise RuntimeError("The account's SSH socket already exists; use an isolated test account")
    with tempfile.TemporaryDirectory(prefix="headroom-ssh-smoke-") as temporary:
        root = Path(temporary)
        config = root / "server.yaml"
        config.write_text("providers:\n" + "".join(
            f"  {provider}: {{enabled: false}}\n" for provider in ("claude", "codex", "cursor", "grok", "opencodego")))
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith(("USAGE_", "CLAUDE_", "CODEX_", "CURSOR_", "GROK_"))}
        environment["USAGE_AUTH_TOKEN"] = "synthetic-backend-token-kept-on-server"
        for provider in ("CLAUDE", "CODEX", "CURSOR", "GROK", "OPENCODEGO"):
            environment[f"USAGE_PROVIDER_{provider}_ENABLED"] = "false"
        backend = daemon = None
        with (root / "server.log").open("wb") as server_log, (root / "sshd.log").open("wb") as ssh_log:
            try:
                backend = subprocess.Popen([
                    server_path, "--config", str(config), "--listen-addr",
                    f"127.0.0.1:{unused_port()}", "--ssh-access"],
                    env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=server_log)
                deadline = time.monotonic() + 10
                while not control.exists():
                    if backend.poll() is not None:
                        raise RuntimeError("Isolated backend exited before its SSH socket was ready")
                    if time.monotonic() > deadline:
                        raise TimeoutError("Isolated backend did not publish its SSH socket")
                    time.sleep(0.05)

                for name in ("host", "client", "wrong-host"):
                    subprocess.run([keygen, "-q", "-t", "ed25519", "-N", "", "-f", str(root / name)], check=True)
                receiver_record = root / "receiver-starts"
                receiver = root / "receiver"
                receiver.write_text(f"#!{sys.executable}\n" +
                    "import os\nfrom pathlib import Path\n" +
                    "if os.environ.get('SSH_ORIGINAL_COMMAND') != 'usage-server --ssh-stdio':\n    raise SystemExit(2)\n" +
                    f"with Path({str(receiver_record)!r}).open('a') as record: record.write('start\\n')\n" +
                    f"os.execv({server_path!r}, [{server_path!r}, '--ssh-stdio'])\n")
                receiver.chmod(0o700)
                port = unused_port()
                ssh_config = root / "sshd_config"
                ssh_config.write_text(
                    f"ListenAddress 127.0.0.1\nPort {port}\nHostKey {root}/host\nPidFile {root}/sshd.pid\n"
                    f"AuthorizedKeysFile {root}/client.pub\nAllowUsers {getpass.getuser()}\n"
                    "PasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM yes\n"
                    # Disposable keys live under /tmp rather than the real account home.
                    "StrictModes no\nAllowAgentForwarding no\nAllowTcpForwarding no\nX11Forwarding no\n"
                    f"ForceCommand {receiver}\nLogLevel ERROR\n")
                daemon = subprocess.Popen([sshd, "-D", "-e", "-f", str(ssh_config)],
                                          stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=ssh_log)
                deadline = time.monotonic() + 5
                while True:
                    if daemon.poll() is not None:
                        raise RuntimeError("Isolated sshd failed to start; inspect the local SSH/PAM prerequisites")
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                            break
                    except OSError:
                        if time.monotonic() > deadline:
                            raise TimeoutError("Isolated sshd did not listen")
                        time.sleep(0.05)

                known_hosts = root / "known_hosts"

                def trust(key_name):
                    key_type, public_key, *_ = (root / (key_name + ".pub")).read_text().split()
                    known_hosts.write_text(f"[127.0.0.1]:{port} {key_type} {public_key}\n")

                trust("host")
                options = ["-F", "none", "-i", str(root / "client"), "-o", "IdentitiesOnly=yes",
                           "-o", "IdentityAgent=none", "-o", f"UserKnownHostsFile={known_hosts}",
                           "-o", "GlobalKnownHostsFile=/dev/null", "-o", "BatchMode=yes",
                           "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=5", "-T"]
                command = [ssh, *options, "-p", str(port), "127.0.0.1", "usage-server --ssh-stdio"]

                def request(method, path, body=b""):
                    frame = json.dumps({"schema": 1, "method": method, "path": path,
                                        "body": base64.b64encode(body).decode()}, separators=(",", ":")).encode() + b"\n"
                    result = subprocess.run(command, input=frame, capture_output=True, timeout=25)
                    if result.returncode:
                        raise RuntimeError("Actual SSH receiver request failed; no remote diagnostics are copied")
                    assert result.stdout.count(b"\n") == 1 and result.stdout.endswith(b"\n")
                    response = json.loads(result.stdout)
                    assert set(response) == {"schema", "status", "body"} and response["schema"] == 1
                    return response["status"], base64.b64decode(response["body"], validate=True)

                status, body = request("GET", "/api/v1/health")
                assert status == 200 and json.loads(body)["status"] == "ok"
                status, body = request("GET", "/api/v1/usage")
                assert status == 200 and json.loads(body) == []
                cookie = b'{"cookie":"sso=synthetic-ssh-smoke-cookie"}'
                status, body = request("PUT", "/api/v1/providers/grok/credentials", cookie)
                assert status == 404 and b"synthetic-ssh-smoke-cookie" not in body

                probe_command = None
                if qt_probe:
                    wrapper = root / "ssh-wrapper"
                    wrapper.write_text(f"#!{sys.executable}\nimport os, sys\n" +
                                       f"os.execv({ssh!r}, {[ssh, *options]!r} + sys.argv[1:])\n")
                    wrapper.chmod(0o700)
                    probe_command = [qt_probe, str(wrapper), f"ssh://127.0.0.1:{port}"]
                    subprocess.run(probe_command, check=True, timeout=45)

                starts = receiver_record.read_text().count("start\n")
                trust("wrong-host")
                rejected = subprocess.run(command, input=b"must-not-reach-receiver\n", capture_output=True, timeout=15)
                assert rejected.returncode != 0 and rejected.stdout == b""
                assert receiver_record.read_text().count("start\n") == starts
                if probe_command:
                    rejected_probe = subprocess.run(probe_command, capture_output=True, timeout=45)
                    assert rejected_probe.returncode != 0, "Qt accepted the changed SSH host key"
                    assert receiver_record.read_text().count("start\n") == starts
                print("Actual OpenSSH -> private socket -> existing Go server: PASS; changed host key rejected")
            finally:
                stop(daemon)
                stop(backend)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", required=True, help="Newly built usage-server executable")
    parser.add_argument("--qt-probe", help="Optional headroom-ssh-probe executable")
    arguments = parser.parse_args()
    run(arguments.server, arguments.qt_probe)
