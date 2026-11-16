#!/usr/bin/env python3
"""Opt-in Linux integration test for an in-place CLI upgrade across releases.

Installs a previously published Headroom CLI release into a disposable install
root, starts its managed `headroom serve` through the public launcher, then has
that release's own updater apply a locally built candidate package: `stage` and
`prepare-apply` are the same transaction `headroom update` uses, including the
manager-owned managed-server participant.

Every provider is disabled, HOME/XDG state and the install root are temporary,
and systemd supervision markers are removed from the environment. Never invoke
a reset action, live credentials, or the user's own installation.
"""
import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

REPOSITORY = "AaronFeledy/headroom"
DOWNLOAD_HOSTS = {"github.com", "raw.githubusercontent.com", "objects.githubusercontent.com",
                  "release-assets.githubusercontent.com"}
PROVIDERS = ("claude", "codex", "cursor", "grok", "opencodego")
MAX_MANIFEST_BYTES = 4 << 20
ACCOUNT_KEY = re.compile(r"^\s*(?:#\s*)?(config_dir|credentials_path):", re.MULTILINE)


def download(url, destination, maximum, attempts=3):
    if not url.startswith("https://"):
        raise AssertionError("refusing a non-HTTPS download")
    failure = None
    for attempt in range(attempts):
        try:
            request = urllib.request.Request(url, headers={"User-Agent": "headroom-cli-upgrade-smoke"})
            with urllib.request.urlopen(request, timeout=30) as response:
                if urllib.parse.urlsplit(response.url).hostname not in DOWNLOAD_HOSTS:
                    raise AssertionError("release download left the documented hosts")
                remaining, digest = maximum, hashlib.sha256()
                with destination.open("wb") as output:
                    while remaining >= 0:
                        chunk = response.read(min(1 << 20, remaining + 1))
                        if not chunk:
                            break
                        if len(chunk) > remaining:
                            raise AssertionError("release download exceeded its bound")
                        digest.update(chunk)
                        output.write(chunk)
                        remaining -= len(chunk)
            destination.chmod(0o600)
            return digest.hexdigest(), destination.stat().st_size
        except (urllib.error.URLError, TimeoutError, OSError) as error:
            failure = error
            if attempt + 1 < attempts:
                time.sleep(2 ** attempt)
    raise AssertionError(f"could not download {url}: {failure}")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            value.update(chunk)
    return value.hexdigest()


def published_package(manifest, version, architecture):
    data = json.loads(manifest.read_text(encoding="utf-8"))
    asset = f"Headroom-CLI-v{version}-linux-{architecture}.tar.gz"
    if (data.get("schema") != 1 or data.get("product") != "Headroom" or data.get("version") != version
            or data.get("package_kind") != "cli"):
        raise AssertionError("release manifest is not the expected CLI manifest")
    matches = [item for item in data.get("packages", []) if item.get("platform") == "linux"
               and item.get("architecture") == architecture and item.get("asset_name") == asset]
    if (len(matches) != 1 or not isinstance(matches[0].get("size"), int) or matches[0]["size"] <= 0
            or not re.fullmatch(r"[0-9a-f]{64}", str(matches[0].get("sha256")))):
        raise AssertionError("release manifest does not describe the expected package")
    return asset, matches[0]["size"], matches[0]["sha256"]


def account_field(example):
    _, marker, tail = example.partition("accounts:")
    if not marker:
        raise AssertionError("published config example has no extra-account format")
    block = []
    for line in tail.splitlines()[1:]:
        if line.strip() and not line.startswith((" ", "\t")):
            break
        block.append(line)
    keys = {match.group(1) for match in ACCOUNT_KEY.finditer("\n".join(block))}
    if len(keys) != 1:
        raise AssertionError(f"published config example has an unrecognized account format: {sorted(keys)}")
    return keys.pop()


def write_config(path, port, field, account_directory):
    account = account_directory if field == "config_dir" else account_directory / ".credentials.json"
    lines = [f"listen_addr: 127.0.0.1:{port}", 'auth_token: ""', "poll_interval: 60s", "providers:"]
    for provider in PROVIDERS:
        lines.extend((f"  {provider}:", "    enabled: false"))
        if provider == "claude":
            lines.extend(("    accounts:", "      - id: work", "        label: Work",
                          f"        {field}: '{account}'", "        enabled: true"))
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def manager(executable, command, arguments, environment, timeout=90):
    completed = subprocess.run([str(executable), command, *map(str, arguments)], env=environment, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    try:
        reply = json.loads(completed.stdout)
    except json.JSONDecodeError:
        raise AssertionError(f"{command} returned invalid JSON: {completed.stdout[-2000:]}{completed.stderr[-2000:]}")
    if (completed.returncode != 0 or reply.get("ok") is not True or reply.get("command") != command
            or not isinstance(reply.get("result"), dict)):
        raise AssertionError(f"{command} failed ({completed.returncode}): {reply.get('error') or completed.stderr[-2000:]}")
    return reply["result"]


def wait_for(condition, deadline, message):
    while not condition():
        if time.monotonic() > deadline:
            raise AssertionError(message)
        time.sleep(0.1)


def reads_json(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None


def healthy(address):
    try:
        with urllib.request.urlopen(address + "/api/v1/health", timeout=1) as response:
            return response.status == 200
    except (urllib.error.URLError, TimeoutError):
        return False


def pid_executable(pid):
    try:
        return Path(os.readlink(f"/proc/{pid}/exe")).resolve()
    except OSError:
        return None


def stop(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def stop_rooted(root, pid):
    if not isinstance(pid, int) or pid <= 0 or pid == os.getpid():
        return
    executable = pid_executable(pid)
    if executable is None or root.resolve() not in executable.parents:
        return
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if not Path(f"/proc/{pid}").exists():
            return
        time.sleep(0.1)
    executable = pid_executable(pid)
    if executable is not None and root.resolve() in executable.parents:
        os.kill(pid, signal.SIGKILL)


def stop_managed_processes(root, transaction):
    record = reads_json(root / "runtime/managed-serve.json")
    stop_rooted(root, record.get("pid") if isinstance(record, dict) else None)
    journal = reads_json(transaction / "apply-journal.json") if transaction is not None else None
    if isinstance(journal, dict):
        stop_rooted(root, journal.get("candidate_pid"))
        stop_rooted(root, journal.get("managed_service_pid"))


def diagnostics(root, transaction, log, error):
    print(f"\nUpgrade fixture failed: {error}", file=sys.stderr)
    if transaction is not None:
        journal = reads_json(transaction / "apply-journal.json")
        if journal is not None:
            print(f"journal phase: {journal.get('phase')}", file=sys.stderr)
            if journal.get("error"):
                print(f"journal error: {str(journal['error'])[-2000:]}", file=sys.stderr)
    for name in ("last-apply-result.json", "runtime/managed-serve.json", "install-state.json"):
        value = reads_json(root / name)
        if value is not None:
            print(f"{name}: {json.dumps(value, sort_keys=True)[:4000]}", file=sys.stderr)
    if log is not None and log.exists():
        print("server stderr tail:", file=sys.stderr)
        print(log.read_text(encoding="utf-8", errors="replace")[-4000:], file=sys.stderr)


def install_release(archive, version, architecture, root, entry, environment):
    base = archive.parent
    extracted = base / "headroom-package"
    member_name = f"{archive.name.removesuffix('.tar.gz')}/bootstrap/headroom-package"
    with tarfile.open(archive, "r:gz") as package:
        members = [member for member in package if member.name == member_name]
        if len(members) != 1 or not members[0].isfile() or not 0 < members[0].size <= 64 << 20:
            raise AssertionError("package bootstrap entry is missing, ambiguous, or unsafe")
        source = package.extractfile(members[0])
        with extracted.open("xb") as output:
            shutil.copyfileobj(source, output)
    extracted.chmod(0o700)
    return manager(extracted, "install", ["--archive", archive, "--install-root", root, "--entry-path", entry,
        "--cli-entry-path", entry, "--version", version, "--platform", "linux", "--arch", architecture,
        "--asset", archive.name], environment)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--from-version", required=True)
    parser.add_argument("--candidate-archive", type=Path, required=True)
    parser.add_argument("--candidate-version", required=True)
    parser.add_argument("--candidate-release-manifest", type=Path)
    args = parser.parse_args()
    from_version = args.from_version.removeprefix("v")
    architecture = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "arm64", "arm64": "arm64"}.get(
        platform.machine().lower())
    if platform.system() != "Linux" or architecture is None:
        raise SystemExit("this fixture requires Linux x86_64 or ARM64")
    candidate = args.candidate_archive.resolve()
    if candidate.name != f"Headroom-CLI-v{args.candidate_version}-linux-{architecture}.tar.gz":
        raise SystemExit("the candidate archive name does not match --candidate-version")
    if args.candidate_release_manifest is not None:
        asset, size, expected = published_package(args.candidate_release_manifest, args.candidate_version, architecture)
        if asset != candidate.name or candidate.stat().st_size != size or digest(candidate) != expected:
            raise SystemExit("the candidate archive does not match its release manifest")

    base = Path(tempfile.mkdtemp(prefix="headroom-cli-upgrade-"))
    root, entry = base / "install", base / "bin/headroom"
    environment = {key: value for key, value in os.environ.items()
        if not key.startswith(("HEADROOM_", "USAGE_", "GITHUB_TOKEN", "GH_TOKEN"))
        and key not in ("INVOCATION_ID", "SYSTEMD_EXEC_PID", "NOTIFY_SOCKET", "DBUS_SESSION_BUS_ADDRESS",
                        "XDG_RUNTIME_DIR", "CLAUDE_CONFIG_DIR")}
    for name in ("home", "config", "data", "cache", "state", "tmp", "bin"):
        (base / name).mkdir()
    environment.update(HOME=str(base / "home"), XDG_CONFIG_HOME=str(base / "config"),
        XDG_DATA_HOME=str(base / "data"), XDG_CACHE_HOME=str(base / "cache"),
        XDG_STATE_HOME=str(base / "state"), TMPDIR=str(base / "tmp"))
    server = watch = handle = transaction = None
    log = base / "server.log"
    try:
        release = f"https://github.com/{REPOSITORY}/releases/download/v{from_version}"
        manifest = base / f"Headroom-CLI-v{from_version}-release.json"
        download(f"{release}/{manifest.name}", manifest, MAX_MANIFEST_BYTES)
        asset, size, expected = published_package(manifest, from_version, architecture)
        archive = base / asset
        actual, actual_size = download(f"{release}/{asset}", archive, size)
        if actual_size != size or actual != expected:
            raise AssertionError("published package digest does not match its release manifest")
        example = base / "config.example.yaml"
        download(f"https://raw.githubusercontent.com/{REPOSITORY}/v{from_version}/server/config.example.yaml",
            example, 256 << 10)
        installed = install_release(archive, from_version, architecture, root, entry, environment)
        state = installed.get("state", installed)  # 3.6.0+ wraps the state in an install result.
        if state.get("active_version") != from_version:
            raise AssertionError("the published release did not become the active installation")
        generation = root / state["version_path"]
        runtime, immutable = generation / "bin/headroom", generation / "bin/headroom-package"

        account_directory = base / ".claude-work"
        account_directory.mkdir()
        config = base / "server.yaml"
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        write_config(config, port, account_field(example.read_text(encoding="utf-8")), account_directory)
        address = f"http://127.0.0.1:{port}"
        handle = log.open("w")
        server = subprocess.Popen([str(entry), "serve", "--config", str(config)], env=environment,
            stdout=handle, stderr=subprocess.STDOUT)
        def ready():
            record = reads_json(root / "runtime/managed-serve.json")
            return isinstance(record, dict) and record.get("ready") is True and record.get("pid") == server.pid
        wait_for(lambda: ready() and healthy(address), time.monotonic() + 30,
            "the published release's provider-disabled server did not become ready")
        record = reads_json(root / "runtime/managed-serve.json")
        if Path(record["executable"]).resolve() != runtime.resolve():
            raise AssertionError("the managed server is not running the published generation")
        if record.get("supervisor"):
            raise AssertionError("the managed server unexpectedly registered a supervisor")

        watch = subprocess.Popen([str(entry), "--watch", "--plain", "--url", address], env=environment,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wait_for(lambda: pid_executable(watch.pid) == runtime.resolve(), time.monotonic() + 30,
            "the update-request CLI did not start from the published generation")
        staged = manager(immutable, "stage", ["--archive", candidate, "--install-root", root,
            "--version", args.candidate_version, "--platform", "linux", "--arch", architecture,
            "--asset", candidate.name], environment)
        if (staged.get("version") != args.candidate_version or staged.get("package_kind") != "cli"
                or staged.get("platform") != "linux" or staged.get("architecture") != architecture):
            raise AssertionError("the candidate stage does not match the requested package")
        stage_record = Path(staged["package_root"]).parent.parent / "verified-stage.json"
        prepared = manager(immutable, "prepare-apply", ["--install-root", root, "--entry-path", entry,
            "--stage-record", stage_record, "--current-pid", watch.pid, "--current-executable", runtime,
            "--current-role", "cli", "--candidate-role", "cli"], environment)
        transaction = Path(prepared["transaction_directory"])
        commit, temporary = Path(prepared["commit_path"]), Path(prepared["commit_path"] + ".tmp")
        with temporary.open("w") as output:
            os.chmod(temporary, 0o600)
            json.dump({"schema": 1, "product": "Headroom", "commit": True, "nonce": prepared["nonce"]}, output)
            output.flush()
            os.fsync(output.fileno())
        temporary.replace(commit)
        stop(watch)
        watch = None

        wait_for(lambda: (root / "last-apply-result.json").is_file(), time.monotonic() + 120,
            "the published release's updater did not finish applying the candidate")
        result = reads_json(root / "last-apply-result.json")
        if result.get("status") != "applied" or result.get("version") != args.candidate_version:
            raise AssertionError(f"apply outcome was {result.get('status')}: "
                f"{result.get('failure_reason') or result.get('error')}")
        state = reads_json(root / "install-state.json")
        if state.get("active_version") != args.candidate_version:
            raise AssertionError("install-state did not activate the candidate")
        wait_for(lambda: healthy(address), time.monotonic() + 30, "the candidate server did not answer health")
        record = reads_json(root / "runtime/managed-serve.json")
        candidate_runtime = (root / state["version_path"] / "bin/headroom").resolve()
        if not (isinstance(record, dict) and record.get("ready") is True
                and Path(record.get("executable", "")).resolve() == candidate_runtime):
            raise AssertionError("the managed server receipt does not describe the candidate")
        if record.get("pid") == server.pid:
            raise AssertionError("the previous server process is still recorded as the managed server")
        try:
            server.wait(timeout=30)
        except subprocess.TimeoutExpired:
            raise AssertionError("the previous server process did not exit")
        print(f"Upgraded Headroom {from_version} to {args.candidate_version} in place; the candidate is ready.")
    except BaseException as error:
        diagnostics(root, transaction, log, error)
        raise
    finally:
        stop(watch)
        stop(server)
        stop_managed_processes(root, transaction)
        if handle is not None:
            handle.close()
        shutil.rmtree(base, ignore_errors=True)


if __name__ == "__main__":
    main()
