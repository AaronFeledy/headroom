import os
from pathlib import Path
import subprocess
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[2] / "install.sh"


class InstallerPlatformTests(unittest.TestCase):
    def dry_run(self, system, architecture, *arguments):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "bin"
            binary.mkdir()
            uname = binary / "uname"
            uname.write_text('#!/bin/sh\ncase "$1" in -s) echo "$TEST_SYSTEM";; -m) echo "$TEST_ARCH";; *) exit 2;; esac\n')
            uname.chmod(0o755)
            home = root / "home with spaces"
            env = os.environ | {"HOME": str(home), "XDG_DATA_HOME": str(root / "xdg data"),
                                "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                                "TEST_SYSTEM": system, "TEST_ARCH": architecture}
            result = subprocess.run(["sh", str(INSTALLER), "--dry-run", *arguments], env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertFalse(home.exists(), "dry run must not create install directories")
            return result

    def test_mac_defaults_for_both_native_architectures(self):
        for architecture in ("x86_64", "arm64"):
            with self.subTest(architecture=architecture):
                result = self.dry_run("Darwin", architecture)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("home with spaces/Library/Application Support/Headroom", result.stdout)
                self.assertIn("home with spaces/Applications/Headroom.app/Contents/MacOS/headroom", result.stdout)

    def test_linux_paths_remain_compatible(self):
        result = self.dry_run("Linux", "x86_64")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("xdg data/headroom", result.stdout)
        self.assertIn("home with spaces/.local/bin/headroom", result.stdout)

    def test_unsupported_targets_do_not_select_another_package(self):
        for system, architecture in (("Darwin", "i386"), ("Linux", "arm64"), ("FreeBSD", "x86_64")):
            with self.subTest(system=system, architecture=architecture):
                result = self.dry_run(system, architecture)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("Would validate", result.stdout)

    def test_cli_supports_linux_arm64_without_a_desktop_entry(self):
        result = self.dry_run("Linux", "aarch64", "--cli")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("home with spaces/.local/bin/headroom", result.stdout)
        self.assertNotIn("headroom-launcher", result.stdout)

    def test_linux_application_entry_follows_custom_root(self):
        result = self.dry_run("Linux", "x86_64", "--install-root", "/tmp/custom root")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("/tmp/custom root/headroom-launcher", result.stdout)


if __name__ == "__main__":
    unittest.main()
