#!/usr/bin/env python3
"""Exercise one packaged usage-server without provider discovery or credentials."""

import argparse
import json
import os
import queue
import secrets
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def request(url, token=None, context=None):
    headers = {"Authorization": "Bearer " + token} if token else {}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                                        urllib.request.HTTPSHandler(context=context))
    try:
        with opener.open(urllib.request.Request(url, headers=headers), timeout=2) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)



def stop(process):
    process.terminate()
    try:
        process.wait(5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(5)


def check_desktop_session(server, environment, creationflags):
    # Public identity is emitted only after the listener belongs to this child.
    # The bearer goes through stdin, never a command argument, env var, or file.
    nonce, token = secrets.token_hex(16), secrets.token_hex(32)
    process = subprocess.Popen([server, "--desktop-session", "--listen-addr", "127.0.0.1:0"],
                               env=environment, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL, creationflags=creationflags)
    try:
        process.stdin.write(json.dumps({"schema": 1, "nonce": nonce, "token": token}).encode() + b"\n")
        process.stdin.close()
        lines = queue.Queue()
        threading.Thread(target=lambda: lines.put(process.stdout.read(16385)), daemon=True).start()
        try:
            line = lines.get(timeout=20)
        except queue.Empty:
            raise TimeoutError("packaged desktop-session identity timed out") from None
        assert line.endswith(b"\n") and line.count(b"\n") == 1 and len(line) <= 16384
        assert token.encode() not in line and b"PRIVATE KEY" not in line
        identity = json.loads(line)
        assert set(identity) == {"schema", "nonce", "address", "certificate"}
        assert identity["schema"] == 1 and identity["nonce"] == nonce
        host, port = identity["address"].rsplit(":", 1)
        assert host == "127.0.0.1" and 0 < int(port) <= 65535
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_verify_locations(cadata=identity["certificate"])
        base = "https://" + identity["address"]
        status, health = request(base + "/api/v1/health", token, context)
        assert status == 200 and health["status"] in ("ok", "degraded")
        assert health["providers"] == [] and health["version"]
        status, usage = request(base + "/api/v1/usage", token, context)
        assert status == 200 and usage == []
        for bad_token in (None, "incorrect-token", environment["USAGE_AUTH_TOKEN"]):
            status, _ = request(base + "/api/v1/health", bad_token, context)
            assert status == 401
        try:
            request(base + "/api/v1/health", context=ssl.create_default_context())
        except urllib.error.URLError as error:
            assert isinstance(error.reason, ssl.SSLCertVerificationError)
        else:
            raise AssertionError("desktop-session certificate unexpectedly used system trust")
    finally:
        stop(process)
        process.stdout.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--server", required=True)
    args = parser.parse_args()
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="headroom-packaged-server-") as root:
        config = os.path.join(root, "config.yaml")
        with open(config, "w", encoding="utf-8") as output:
            output.write("providers:\n  claude: {enabled: false}\n  codex: {enabled: false}\n  cursor: {enabled: false}\n  grok: {enabled: false}\n  opencodego: {enabled: false}\n")
        environment = dict(os.environ)
        for key in list(environment):
            if key.startswith("USAGE_") or key.startswith("CLAUDE_") or key.startswith("CODEX_") or key.startswith("CURSOR_") or key.startswith("GROK_"):
                environment.pop(key)
        environment.update({
            "USAGE_CONFIG": config,
            "USAGE_AUTH_TOKEN": "package-smoke-token",
            "USAGE_PROVIDER_CLAUDE_ENABLED": "false",
            "USAGE_PROVIDER_CODEX_ENABLED": "false",
            "USAGE_PROVIDER_CURSOR_ENABLED": "false",
            "USAGE_PROVIDER_GROK_ENABLED": "false",
            "USAGE_PROVIDER_OPENCODEGO_ENABLED": "false",
            "HOME": root,
        })
        creationflags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        process = subprocess.Popen([args.server, "--listen-addr", f"127.0.0.1:{port}"], env=environment,
                                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                   stderr=subprocess.DEVNULL, creationflags=creationflags)
        try:
            deadline = time.monotonic() + 20
            while True:
                if process.poll() is not None:
                    raise RuntimeError(f"packaged server exited {process.returncode}")
                try:
                    status, health = request(f"http://127.0.0.1:{port}/api/v1/health", "package-smoke-token")
                    if status == 200:
                        break
                except OSError:
                    pass
                if time.monotonic() >= deadline:
                    raise TimeoutError("packaged server readiness timed out")
                time.sleep(0.05)
            assert health["status"] in ("ok", "degraded") and health["providers"] == [] and health["version"]
            status, usage = request(f"http://127.0.0.1:{port}/api/v1/usage", "package-smoke-token")
            assert status == 200 and usage == []
            status, _ = request(f"http://127.0.0.1:{port}/api/v1/health")
            assert status == 401
        finally:
            stop(process)
        check_desktop_session(args.server, environment, creationflags)


if __name__ == "__main__":
    main()
