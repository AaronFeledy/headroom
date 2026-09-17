import json
import os
from pathlib import Path
import plistlib
import struct
import subprocess
import tempfile
import unittest
import zlib

ROOT = Path(__file__).resolve().parents[2]
INSTALLER = (ROOT / "install.sh").read_text()


def run_entry(name, root, entry, home, private, *arguments, xdg=True):
    start = INSTALLER.index(name + "() {\n")
    end = INSTALLER.index("\n}\n", start) + 3
    function = INSTALLER[start:end]
    environment = os.environ | {"HOME": str(home), "install_root": str(root),
        "entry_path": str(entry), "private_root": str(private)}
    environment.pop("XDG_DATA_HOME", None)
    if xdg:
        environment["XDG_DATA_HOME"] = str(home / "user data")
    subprocess.run(["sh", "-c", function + '\n' + name + ' "$@"', "fixture", *arguments],
                   env=environment, check=True, capture_output=True, text=True)


class InstalledEntryTests(unittest.TestCase):
    def test_linux_icon_and_entry_survive_generation_change(self):
        for xdg in (False, True):
            with self.subTest(xdg=xdg), tempfile.TemporaryDirectory() as temporary:
                home = Path(temporary)
                root, private = home / "install with spaces", home / "private"
                root.mkdir(); private.mkdir()
                entry = root / "headroom-launcher"
                entry.write_text("stable launcher")
                data = home / ("user data" if xdg else ".local/share")
                desktop = data / "applications/headroom.desktop"
                icon = data / "icons/hicolor/scalable/apps/headroom.svg"
                first_entry = None
                for number in (1, 2):
                    generation = Path("versions") / str(number)
                    asset = root / generation / "share/icons/hicolor/scalable/apps/headroom.svg"
                    asset.parent.mkdir(parents=True)
                    contents = '<svg xmlns="http://www.w3.org/2000/svg"><!-- %d --></svg>' % number
                    asset.write_text(contents)
                    (root / "install-state.json").write_text(json.dumps({"version_path": str(generation)}))
                    run_entry("linux_entry", root, entry, home, private, xdg=xdg)
                    text = desktop.read_text()
                    self.assertIn('Exec="' + str(entry) + '"\n', text)
                    self.assertIn("Icon=headroom\n", text)
                    self.assertIn("StartupWMClass=Headroom\n", text)
                    self.assertEqual(icon.read_text(), contents)
                    if first_entry is not None:
                        self.assertEqual(text, first_entry)
                    first_entry = text
                    asset.unlink()  # Removing an old generation cannot break the launcher icon.
                    self.assertEqual(icon.read_text(), contents)

    def test_mac_finder_identity_and_icon_survive_generation_change(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            root, private = home / "install", home / "private"
            root.mkdir(); private.mkdir()
            entry = home / "Applications/Headroom.app/Contents/MacOS/headroom"
            entry.parent.mkdir(parents=True)
            entry.write_text("stable launcher")
            contents = entry.parent.parent
            first_info = None
            for number in (1, 2):
                generation = Path("versions") / str(number)
                source = root / generation / "Headroom.app/Contents/Resources/headroom.icns"
                source.parent.mkdir(parents=True)
                source.write_bytes(b"synthetic icon " + bytes([number]))
                (root / "install-state.json").write_text(json.dumps({"version_path": str(generation)}))
                run_entry("macos_entry", root, entry, home, private, "check")
                run_entry("macos_entry", root, entry, home, private, "write")
                info = plistlib.loads((contents / "Info.plist").read_bytes())
                self.assertEqual(info["CFBundleIdentifier"], "io.headroom.launcher")
                self.assertEqual(info["CFBundleExecutable"], "headroom")
                self.assertEqual(info["CFBundleIconFile"], "headroom.icns")
                self.assertTrue(info["LSUIElement"])
                icon = contents / "Resources" / info["CFBundleIconFile"]
                self.assertEqual(icon.read_bytes(), source.read_bytes())
                if first_info is not None:
                    self.assertEqual(info, first_info)
                first_info = info
                source.unlink()
                self.assertTrue(icon.is_file())

    def test_mac_does_not_overwrite_another_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            entry = home / "Foreign.app/Contents/MacOS/headroom"
            entry.parent.mkdir(parents=True)
            info = entry.parent.parent / "Info.plist"
            data = plistlib.dumps({"CFBundleIdentifier": "some.other.app"})
            info.write_bytes(data)
            with self.assertRaises(subprocess.CalledProcessError):
                run_entry("macos_entry", home, entry, home, home, "check")
            self.assertEqual(info.read_bytes(), data)


class WindowsIconTests(unittest.TestCase):
    def test_all_icon_sizes_have_transparent_corners(self):
        data = (ROOT / "clients/desktop/windows/headroom.ico").read_bytes()
        reserved, kind, count = struct.unpack_from("<HHH", data)
        self.assertEqual((reserved, kind), (0, 1))
        sizes = set()
        for index in range(count):
            width, height, _, _, _, _, length, offset = struct.unpack_from("<BBBBHHII", data, 6 + index*16)
            width, height = width or 256, height or 256
            self.assertEqual(width, height)
            sizes.add(width)
            png = data[offset:offset+length]
            self.assertEqual(png[:8], b"\x89PNG\r\n\x1a\n")
            compressed = bytearray()
            cursor = 8
            while cursor < len(png):
                size = struct.unpack_from(">I", png, cursor)[0]
                name, payload = png[cursor+4:cursor+8], png[cursor+8:cursor+8+size]
                if name == b"IHDR":
                    self.assertEqual(struct.unpack_from(">IIBB", payload), (width, height, 8, 6))
                elif name == b"IDAT": compressed.extend(payload)
                cursor += size + 12
            pixels = zlib.decompress(compressed)
            # At the first pixel of the first scanline, all PNG filter predictors
            # are zero, so its alpha byte is directly readable without a decoder.
            self.assertIn(pixels[0], range(5))
            self.assertEqual(pixels[4], 0, f"opaque corner at {width}px")
        self.assertEqual(sizes, {16, 24, 32, 48, 64, 128, 256})


if __name__ == "__main__":
    unittest.main()
