import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location(
    "headroom_macos_smoke", Path(__file__).with_name("macos_package_smoke.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

PNG_START = b"\x89PNG\r\n\x1a\n"
PNG_END = b"\x00\x00\x00\x00IEND\xaeB\x60\x82"


class MacCaptureCompletionTests(unittest.TestCase):
    def test_waits_for_async_image_completion_and_readiness(self):
        with tempfile.TemporaryDirectory() as temporary:
            image = Path(temporary) / "capture.png"
            ready = image.with_suffix(".ready.json")
            clock = [0.0]
            steps = []

            def advance(seconds):
                clock[0] += seconds
                steps.append(seconds)
                if len(steps) == 1:
                    image.write_bytes(PNG_START)
                elif len(steps) == 2:
                    image.write_bytes(PNG_START + PNG_END)
                elif len(steps) == 3:
                    ready.write_text("{}")

            with mock.patch.object(MODULE.time, "monotonic", side_effect=lambda: clock[0]), \
                    mock.patch.object(MODULE.time, "sleep", side_effect=advance):
                MODULE.wait_for_capture(image, ready, timeout=1)
            self.assertEqual(len(steps), 3)

    def test_incomplete_outputs_fail_with_bounded_timeout(self):
        for contents, has_ready in [(None, False), (b"", True),
                                    (PNG_START, True), (PNG_START + PNG_END, False)]:
            with self.subTest(contents=contents, has_ready=has_ready), \
                    tempfile.TemporaryDirectory() as temporary:
                image = Path(temporary) / "capture.png"
                ready = image.with_suffix(".ready.json")
                if contents is not None:
                    image.write_bytes(contents)
                if has_ready:
                    ready.write_text("{}")
                clock = [0.0]

                def advance(seconds):
                    clock[0] += seconds

                with mock.patch.object(MODULE.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(MODULE.time, "sleep", side_effect=advance):
                    with self.assertRaisesRegex(RuntimeError, "did not complete capture and readiness"):
                        MODULE.wait_for_capture(image, ready, timeout=0.2)
                self.assertAlmostEqual(clock[0], 0.2)


if __name__ == "__main__":
    unittest.main()
