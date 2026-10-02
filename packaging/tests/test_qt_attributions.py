import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest
import zipfile


MODULE_PATH = pathlib.Path(__file__).parents[1] / "qt_attributions.py"
SPEC = importlib.util.spec_from_file_location("headroom_qt_attributions", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
SPEC.loader.exec_module(MODULE)


class QtAttributionTests(unittest.TestCase):
    def fixture(self, root: pathlib.Path, missing_reference=False):
        archive_name = "qtbase-everywhere-src-6.12.0.zip"
        archive = root / archive_name
        prefix = archive_name.removesuffix(".zip")
        attribution = (
            '[{"Id":"fixture","Name":"Fixture\nLibrary","LicenseId":"MIT",'
            '"LicenseFile":"LICENSE.txt","QtUsage":"Used in Qt Core."}]'
        )
        with zipfile.ZipFile(archive, "w") as output:
            output.writestr(prefix + "/LICENSES/MIT.txt", "MIT\n")
            output.writestr(prefix + "/src/thirdparty/qt_attribution.json", attribution)
            if not missing_reference:
                output.writestr(prefix + "/src/thirdparty/LICENSE.txt", "fixture license\n")
        source = {
            "module": "qtbase", "archive": archive_name,
            "url": "https://download.qt.io/archive/qt/6.12/6.12.0/submodules/" + archive_name,
            "size": archive.stat().st_size,
            "sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
        }
        return {"schema": 1, "qt_version": "6.12.0", "sources": [source]}

    def test_collects_literal_newline_json_references_and_payload_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            manifest = self.fixture(root)
            qt_root, payload, output = root / "qt", root / "payload", root / "output"
            qt_root.mkdir(); payload.mkdir()
            (payload / "Qt6Core.dll").write_bytes(b"runtime")
            MODULE.collect(manifest, root, qt_root, payload, output, ["qtbase"])
            index = json.loads((output / "index.json").read_text(encoding="utf-8"))
            self.assertEqual(index["modules"][0]["payload_role"], "deployed_runtime")
            self.assertEqual(len(index["modules"][0]["attribution_records"]), 1)
            self.assertTrue((output / "sources/qtbase/src/thirdparty/LICENSE.txt").is_file())
            self.assertFalse(index["kit_sbom_present"])

    def test_rejects_hash_mismatch_and_missing_referenced_license(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            manifest = self.fixture(root)
            bad = dict(manifest["sources"][0]); bad["sha256"] = "0" * 64
            with self.assertRaises(ValueError):
                MODULE.verify_archive(root / bad["archive"], bad)
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            manifest = self.fixture(root, missing_reference=True)
            (root / "qt").mkdir(); (root / "payload").mkdir()
            with self.assertRaises(ValueError):
                MODULE.collect(manifest, root, root / "qt", root / "payload", root / "output", ["qtbase"])

    def test_qtsvg_without_third_party_records_preserves_module_licenses(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            archive_name = "qtsvg-everywhere-src-6.12.0.zip"
            archive = root / archive_name
            prefix = archive_name.removesuffix(".zip")
            with zipfile.ZipFile(archive, "w") as output:
                output.writestr(prefix + "/LICENSES/LGPL-3.0-only.txt", "module license\n")
                output.writestr(prefix + "/REUSE.toml", "version = 1\n")
                output.writestr(prefix + "/licenseRule.json", "{}\n")
            source = {"module": "qtsvg", "archive": archive_name,
                      "url": "https://download.qt.io/archive/qt/6.12/6.12.0/submodules/" + archive_name,
                      "size": archive.stat().st_size,
                      "sha256": hashlib.sha256(archive.read_bytes()).hexdigest()}
            manifest = {"schema": 1, "qt_version": "6.12.0", "sources": [source]}
            qt_root, payload, output = root / "qt", root / "payload", root / "output"
            qt_root.mkdir(); payload.mkdir()
            MODULE.collect(manifest, root, qt_root, payload, output, ["qtsvg"])
            self.assertEqual((output / "sources/qtsvg/LICENSES/LGPL-3.0-only.txt").read_text(), "module license\n")
            self.assertTrue((output / "sources/qtsvg/REUSE.toml").is_file())
            with self.assertRaises(ValueError):
                MODULE.collect({**manifest, "qt_version": "6.13.0"}, root, qt_root, payload,
                               root / "unexpected", ["qtsvg"])

    def test_notice_paths_are_not_runtime_payload_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            payload = pathlib.Path(temporary)
            notice = payload / "share/licenses/qt/attributions/sources/qtwayland/qml/NOTICE.txt"
            notice.parent.mkdir(parents=True)
            notice.write_text("notice only\n", encoding="utf-8")
            self.assertEqual(MODULE.payload_matches("qtwayland", payload), [])


if __name__ == "__main__":
    unittest.main()
