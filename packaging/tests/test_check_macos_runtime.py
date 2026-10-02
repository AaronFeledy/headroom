import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "headroom_macos_runtime", Path(__file__).parents[1] / "check_macos_runtime.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

MODERN = """Load command 8
      cmd LC_BUILD_VERSION
  cmdsize 32
 platform 1
    minos 14.4
      sdk 15.2
   ntools 1
     tool 3
  version 1115.7.3
Load command 9
      cmd LC_SOURCE_VERSION
  version 0.0
"""


class MacRuntimeTests(unittest.TestCase):
    def check_headers(self, headers, slices="x86_64 arm64\n", architecture="arm64"):
        with patch.object(MODULE.subprocess, "check_output", side_effect=[slices, headers]):
            MODULE.check(Path("Headroom"), architecture)

    def test_deployment_target_ignores_linker_and_sdk_versions(self):
        self.check_headers(MODERN)

    def test_legacy_deployment_command(self):
        self.check_headers("Load command 1\n cmd LC_VERSION_MIN_MACOSX\n version 11.0\n sdk 15.2\n")

    def test_rejects_newer_missing_duplicate_and_non_macos_targets(self):
        for headers in (MODERN.replace("minos 14.4", "minos 14.5"),
                        MODERN.replace("minos 14.4", "minos 15.0"),
                        MODERN.replace("minos 14.4", ""), MODERN + MODERN,
                        MODERN.replace("platform 1", "platform 2")):
            with self.subTest(headers=headers), self.assertRaises(ValueError):
                self.check_headers(headers)

    def test_rejects_missing_architecture(self):
        with self.assertRaisesRegex(ValueError, "missing arm64"):
            self.check_headers(MODERN, slices="x86_64\n")


if __name__ == "__main__":
    unittest.main()
