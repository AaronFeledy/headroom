import json
from functools import cached_property
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from typing import Callable, TypedDict
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
CORPUS = ROOT / "packaging/fixtures/readiness.json"


class MarkerContent(TypedDict, total=False):
    raw: str
    marker: dict[str, str | int | float | bool | None | list[str]]


class MarkerCase(MarkerContent):
    name: str
    accept: bool


class MarkerCorpus(TypedDict):
    nonce: str
    version: str
    cases: list[MarkerCase]


decode_corpus: Callable[[str], MarkerCorpus] = json.loads


def shell_wait_code() -> str:
    source = (ROOT / "install.sh").read_text()
    match = re.search(
        r'python3 - "\$ready" "\$install_root" "\$nonce" "\$platform" <<\'PY\'\n(.*?)\nPY',
        source, re.S,
    )
    if match is None:
        raise AssertionError("Installer readiness Python block is missing")
    return match.group(1)


class ShellReadinessTests(unittest.TestCase):
    @cached_property
    def root(self) -> Path:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name) / "install with spaces"
        root.mkdir()
        _ = (root / "install-state.json").write_text(json.dumps({"active_version": "1.2.3+Build.A", "version_path": "versions/generation"}))
        return root

    @property
    def executable(self) -> Path:
        return self.root / "versions/generation/bin/headroom"

    @property
    def ready(self) -> Path:
        return self.root / "ready.json"

    @property
    def state(self) -> Path:
        return self.root / "install-state.json"

    def run_wait(self, markers: list[str]) -> tuple[int, list[float]]:
        _ = self.ready.write_text(markers[0])
        elapsed = [0.0]
        sleeps: list[float] = []

        def sleep(seconds: float) -> None:
            sleeps.append(seconds)
            elapsed[0] += 1.0
            _ = self.ready.write_text(markers[min(len(sleeps), len(markers) - 1)])
            # Waiting must stay pinned even when another install changes state.
            _ = self.state.write_text(json.dumps({"active_version": "9.9.9", "version_path": "versions/other"}))

        with patch.object(sys, "argv", ["installer", str(self.ready), str(self.root), "Nonce-AbC", "linux"]), \
             patch("time.monotonic", side_effect=lambda: elapsed[0]), patch("time.sleep", side_effect=sleep):
            try:
                exec(compile(shell_wait_code(), "install.sh readiness", "exec"), {"__name__": "__main__"})
            except SystemExit as result:
                return (result.code if isinstance(result.code, int) else int(result.code is not None)), sleeps
        return 0, sleeps

    def test_shared_marker_corpus(self) -> None:
        corpus = decode_corpus(CORPUS.read_text())
        for case in corpus["cases"]:
            with self.subTest(case=case["name"]):
                _ = self.state.write_text(json.dumps({"active_version": corpus["version"], "version_path": "versions/generation"}))
                raw = case.get("raw", json.dumps(case.get("marker")))
                encoded = json.dumps(str(self.executable))
                raw = raw.replace("@EXE_JSON@", encoded).replace("@EXE@", encoded[1:-1])
                status, _ = self.run_wait([raw])
                self.assertEqual(status == 0, case["accept"])

    def test_wrong_then_correct_marker_stays_pinned(self) -> None:
        valid = json.dumps({"nonce": "Nonce-AbC", "version": "1.2.3+Build.A", "pid": 42, "executable": str(self.executable)})
        status, sleeps = self.run_wait(["{", valid.replace("Nonce-AbC", "wrong"), valid])
        self.assertEqual((status, len(sleeps)), (0, 2))

    def test_timeout_uses_elapsed_budget(self) -> None:
        status, sleeps = self.run_wait(["{}"])
        self.assertEqual((status, len(sleeps)), (1, 10))

    def test_oversized_marker_is_pending(self) -> None:
        status, _ = self.run_wait([json.dumps({"nonce": "Nonce-AbC", "version": "1.2.3+Build.A", "pid": 42,
                                              "executable": str(self.executable), "padding": "x" * 4096})])
        self.assertEqual(status, 1)

    def test_real_interpreter_missing_marker_times_out(self) -> None:
        result = subprocess.run([sys.executable, "-c", shell_wait_code(), str(self.ready), str(self.root), "Nonce-AbC", "linux"],
                                capture_output=True, text=True, timeout=15)
        self.assertEqual((result.returncode, result.stderr), (1, ""))

    def test_shell_resolves_symlinks_but_keeps_case_sensitive_paths(self) -> None:
        link = self.root / "linked headroom"
        self.executable.parent.mkdir(parents=True)
        self.executable.touch()
        link.symlink_to(self.executable)
        for path, accepted in ((link, True), (str(self.executable).upper(), False)):
            with self.subTest(path=path):
                _ = self.state.write_text(json.dumps({"active_version": "1.2.3+Build.A", "version_path": "versions/generation"}))
                status, _ = self.run_wait([json.dumps({"nonce": "Nonce-AbC", "version": "1.2.3+Build.A", "pid": 42, "executable": str(path)})])
                self.assertEqual(status == 0, accepted)


class PowerShellReadinessTests(unittest.TestCase):
    def test_shipped_polling_corpus_and_transition(self) -> None:
        pwsh = shutil.which("pwsh") or ("/snap/bin/pwsh" if Path("/snap/bin/pwsh").exists() else None)
        if pwsh is None:
            self.skipTest("PowerShell Core is unavailable")
        result = subprocess.run([pwsh, "-NoProfile", "-File", str(ROOT / "packaging/tests/readiness_fixture.ps1"),
                                 "-RepositoryRoot", str(ROOT)], capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    _ = unittest.main()
