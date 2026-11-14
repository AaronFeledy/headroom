import os
from functools import cached_property
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Final
import unittest


ROOT: Final = Path(__file__).resolve().parents[2]
HELPER: Final = ROOT / "packaging/ci/install-apt.sh"
FIXTURES: Final = Path(__file__).resolve().parent / "fixtures/ci-apt"


@unittest.skipUnless(os.name == "posix" and shutil.which("bash"), "requires bash")
class CIAptTests(unittest.TestCase):
    @cached_property
    def root(self) -> Path:
        temporary = tempfile.TemporaryDirectory(dir=ROOT / "packaging/tests")
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        binary = root / "bin"
        binary.mkdir()
        bash = shutil.which("bash")
        assert bash is not None
        (binary / "bash").symlink_to(bash)
        for name in ("apt-get", "timeout"):
            target = binary / name
            _ = shutil.copyfile(FIXTURES / name, target)
            target.chmod(0o755)
        for phase in ("update", "download", "install", "timeout"):
            _ = (root / f"{phase}-statuses").write_text("0\n", encoding="utf-8")
        return root

    @property
    def binary(self) -> Path:
        return self.root / "bin"

    @cached_property
    def env(self) -> dict[str, str]:
        return {"PATH": str(self.binary), "APT_FIXTURE": str(self.root)}

    def statuses(self, phase: str, values: tuple[int, ...]) -> None:
        _ = (self.root / f"{phase}-statuses").write_text(
            "\n".join(map(str, values)) + "\n", encoding="utf-8"
        )

    def run_helper(self) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(self.binary / "bash"), str(HELPER), "fixture-package", "another-package"],
            env=self.env, capture_output=True, text=True, timeout=10,
        )

    def lines(self, name: str) -> list[str]:
        path = self.root / name
        return path.read_text(encoding="utf-8").splitlines() if path.exists() else []

    def test_success_installs_requested_packages_from_prepared_cache(self) -> None:
        result = self.run_helper()

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.lines("installed"), ["fixture-package", "another-package"])
        self.assertEqual(self.lines("operations"), ["update", "download", "install"])
        self.assertEqual(self.lines("deadlines"), ["download", "install"])

    def test_preparation_failure_propagates_without_mutation(self) -> None:
        for phase in ("update", "download"):
            for status in (100, 127, 130, 137, 143):
                with self.subTest(phase=phase, status=status):
                    self.statuses(phase, (status,))
                    result = self.run_helper()

                    self.assertEqual(result.returncode, status, result.stderr)
                    self.assertEqual(self.lines("installed"), [])
                    self.assertNotIn("install", self.lines("operations"))
                    self.statuses(phase, (0,))

    def test_graceful_preparation_timeout_recovers_once(self) -> None:
        self.statuses("download", (124, 0))

        result = self.run_helper()

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.lines("installed"), ["fixture-package", "another-package"])
        self.assertEqual(self.lines("operations"),
                         ["update", "download", "update", "download", "install"])
        self.assertEqual(self.lines("deadlines"), ["download", "download", "install"])

    def test_timeout_exhaustion_and_monitor_errors_stop_preparation(self) -> None:
        for statuses in ((124, 124), (137,), (100,), (127,), (130,), (143,)):
            with self.subTest(statuses=statuses):
                self.statuses("timeout", statuses)
                _ = (self.root / "deadlines").write_text("", encoding="utf-8")

                result = self.run_helper()

                self.assertEqual(result.returncode, statuses[-1], result.stderr)
                self.assertEqual(self.lines("installed"), [])
                self.assertEqual(self.lines("deadlines"), ["download"] * len(statuses))

    def test_mutation_errors_and_timeouts_are_never_retried(self) -> None:
        for status in (100, 124, 127, 130, 137, 143):
            with self.subTest(status=status):
                self.statuses("install", (status, 0))
                _ = (self.root / "operations").write_text("", encoding="utf-8")

                result = self.run_helper()

                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(self.lines("installed"), [])
                self.assertEqual(self.lines("operations"), ["update", "download", "install"])

    def test_real_gnu_timeout_terminates_disposable_apt_without_mutation_retry(self) -> None:
        timeout = shutil.which("timeout")
        if timeout is None:
            self.skipTest("requires GNU timeout")
        self.env |= {"APT_REAL_TIMEOUT": timeout, "APT_HANG_PHASE": "install"}
        for ignores_term, expected in (("0", 124), ("1", 137)):
            with self.subTest(ignores_term=ignores_term):
                self.env["APT_IGNORE_TERM"] = ignores_term
                _ = (self.root / "operations").write_text("", encoding="utf-8")

                result = self.run_helper()

                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual(self.lines("installed"), [])
                self.assertEqual(self.lines("operations"), ["update", "download", "install"])


if __name__ == "__main__":
    _ = unittest.main()
