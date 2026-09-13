import importlib.util
from pathlib import Path
import struct
import tempfile
import unittest
import zlib


SPEC = importlib.util.spec_from_file_location(
    "headroom_macos_gui_evidence", Path(__file__).resolve().parent / "macos_gui_evidence.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def write_png(path: Path, width: int, height: int, rgba: bytes, color_type=6):
    channels = 4 if color_type == 6 else 3
    raw = bytearray()
    stride = width * channels
    for row in range(height):
        raw.append(0)
        raw.extend(rgba[row * stride:(row + 1) * stride])
    ihdr = struct.pack(">IIBBBBB", width, height, 8, color_type, 0, 0, 0)
    chunks = [(b"IHDR", ihdr), (b"IDAT", zlib.compress(bytes(raw), 9)), (b"IEND", b"")]
    data = bytearray(MODULE.PNG_SIGNATURE)
    for name, payload in chunks:
        data.extend(struct.pack(">I", len(payload)))
        data.extend(name)
        data.extend(payload)
        data.extend(struct.pack(">I", zlib.crc32(name + payload) & 0xFFFFFFFF))
    path.write_bytes(data)


class MacGuiEvidenceTests(unittest.TestCase):
    def test_installer_manifest_uses_archive_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / "Headroom-v2.0.1-macos-arm64.tar.gz"
            archive.write_bytes(b"synthetic-package")
            manifest = MODULE.installer_manifest(archive, "2.0.1", "arm64")
            self.assertEqual(manifest["schema"], 1)
            self.assertEqual(manifest["product"], "Headroom")
            self.assertEqual(manifest["packages"][0]["asset_name"], archive.name)
            self.assertEqual(manifest["packages"][0]["size"], len(b"synthetic-package"))
            self.assertEqual(len(manifest["packages"][0]["sha256"]), 64)

    def test_rendered_png_rejects_blank_and_tiny_frames(self):
        with tempfile.TemporaryDirectory() as temporary:
            blank = Path(temporary) / "blank.png"
            pixels = bytes([12, 14, 18, 255]) * (500 * 500)
            write_png(blank, 500, 500, pixels)
            with self.assertRaises(AssertionError):
                MODULE.assert_rendered_png(blank)
            tiny = Path(temporary) / "tiny.png"
            write_png(tiny, 20, 20, bytes([255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255] * 134))
            with self.assertRaises(AssertionError):
                MODULE.assert_rendered_png(tiny)

    def test_rendered_png_accepts_a_varied_gui_sized_frame(self):
        with tempfile.TemporaryDirectory() as temporary:
            image = Path(temporary) / "headroom.png"
            pixels = bytearray()
            for row in range(480):
                for column in range(640):
                    if row < 80:
                        pixels.extend((248, 248, 252, 255))
                    elif 200 <= row < 260 and 220 <= column < 420:
                        pixels.extend((180, 150, 255, 255))
                    else:
                        pixels.extend((28, 30, 42, 255))
            write_png(image, 640, 480, bytes(pixels))
            header = MODULE.assert_rendered_png(image, minimum_bytes=2000)
            self.assertEqual(header["width"], 640)
            self.assertEqual(header["height"], 480)
            self.assertGreaterEqual(header["quantized_colors"], 3)

    def test_smoke_helpers_never_invoke_reset_credit_entry_points(self):
        root = Path(__file__).resolve().parent
        for name in ("macos_gui_evidence.py", "macos_package_smoke.py", "macos_finder_smoke.py"):
            text = (root / name).read_text(encoding="utf-8")
            self.assertNotRegex(text, r"consumeChatGptReset\(")
            self.assertNotRegex(text, r"ConsumeResetCredit\s*\(")
            self.assertNotRegex(text, r"/api/v1/[^.\s\"']*reset")


if __name__ == "__main__":
    unittest.main()
