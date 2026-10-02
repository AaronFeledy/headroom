import importlib.util
import os
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location(
    "headroom_package_macos", Path(__file__).parents[1] / "package-macos.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MacAssemblyTests(unittest.TestCase):
    def test_canonical_bundle_name_preserves_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "headroom.app").mkdir()
            (root / "headroom.app/payload").write_text("fixture")
            app = MODULE.canonical_app_bundle(root)
            self.assertEqual([path.name for path in root.iterdir()], ["Headroom.app"])
            self.assertEqual((app / "payload").read_text(), "fixture")

    def test_qml_plugins_use_central_location_at_nested_depths(self):
        with tempfile.TemporaryDirectory() as temporary:
            app = Path(temporary)
            plugins = app / "Contents/PlugIns"
            qml = app / "Contents/Resources/qml"
            plugins.mkdir(parents=True)
            shallow = qml / "QtQuick"
            nested = qml / "QtQuick/Controls/Basic/impl"
            shallow.mkdir(parents=True)
            nested.mkdir(parents=True)
            (shallow / "qmldir").write_text("module QtQuick\nplugin quickplugin\n")
            (nested / "qmldir").write_text(
                "module QtQuick.Controls.Basic.impl\noptional plugin basicimplplugin\n")
            for directory, name in ((shallow, "quickplugin"), (nested, "basicimplplugin")):
                target = plugins / f"lib{name}.dylib"
                target.write_bytes(b"synthetic plugin")
                if directory == shallow:
                    (directory / target.name).symlink_to(
                        Path(os.path.relpath(target, directory)))
                else:
                    # A second macdeployqt pass can replace Qt's link with a
                    # separately patched regular copy.
                    (directory / target.name).write_bytes(b"patched plugin")

            MODULE.relocate_qml_plugins(app)

            self.assertEqual((shallow / "qmldir").read_text(),
                "module QtQuick\nplugin quickplugin ../../../PlugIns\n")
            self.assertEqual((nested / "qmldir").read_text(),
                "module QtQuick.Controls.Basic.impl\n"
                "optional plugin basicimplplugin ../../../../../../PlugIns\n")
            self.assertFalse((shallow / "libquickplugin.dylib").exists())
            self.assertFalse((nested / "libbasicimplplugin.dylib").exists())

    def test_qml_plugins_can_be_deployed_only_in_central_location(self):
        # Qt 6.12 installs the real plugins under PlugIns and removes their
        # build-tree copies/links under Resources/qml before macdeployqt runs.
        with tempfile.TemporaryDirectory() as temporary:
            app = Path(temporary)
            plugins = app / "Contents/PlugIns"
            plugins.mkdir(parents=True)
            for module, name, prefix, relative in (
                ("QtQuick", "qtquick2plugin", "", "../../../PlugIns"),
                ("QtQml/Models", "modelsplugin", "optional ", "../../../../PlugIns"),
            ):
                directory = app / "Contents/Resources/qml" / module
                directory.mkdir(parents=True)
                (directory / "qmldir").write_text(
                    f"module {module.replace('/', '.')}\n{prefix}plugin {name}\n")
                (plugins / f"lib{name}.dylib").write_bytes(b"deployed plugin")
            framework = app / "Contents/Frameworks/QtQml.framework"
            (framework / "Versions/A").mkdir(parents=True)
            (framework / "Versions/A/QtQml").write_bytes(b"framework")
            (framework / "Versions/Current").symlink_to("A")
            (framework / "QtQml").symlink_to("Versions/Current/QtQml")

            MODULE.relocate_qml_plugins(app)

            for module, name, prefix, relative in (
                ("QtQuick", "qtquick2plugin", "", "../../../PlugIns"),
                ("QtQml/Models", "modelsplugin", "optional ", "../../../../PlugIns"),
            ):
                directory = app / "Contents/Resources/qml" / module
                self.assertEqual((directory / "qmldir").read_text(),
                    f"module {module.replace('/', '.')}\n{prefix}plugin {name} {relative}\n")
                self.assertEqual((plugins / f"lib{name}.dylib").read_bytes(), b"deployed plugin")
                self.assertFalse((directory / f"lib{name}.dylib").exists())
            self.assertEqual(os.readlink(framework / "Versions/Current"), "A")
            self.assertEqual(os.readlink(framework / "QtQml"), "Versions/Current/QtQml")

    def test_qml_central_plugin_must_be_a_regular_file(self):
        for layout in ("missing", "directory", "symlink", "broken symlink"):
            with self.subTest(layout=layout), tempfile.TemporaryDirectory() as temporary:
                app = Path(temporary)
                plugins = app / "Contents/PlugIns"
                plugins.mkdir(parents=True)
                directory = app / "Contents/Resources/qml/QtQml/Models"
                directory.mkdir(parents=True)
                original = "module QtQml.Models\noptional plugin modelsplugin\n"
                (directory / "qmldir").write_text(original)
                central = plugins / "libmodelsplugin.dylib"
                if layout == "directory":
                    central.mkdir()
                elif layout in ("symlink", "broken symlink"):
                    target = plugins / "other.dylib"
                    if layout == "symlink":
                        target.write_bytes(b"other plugin")
                    central.symlink_to(target.name)
                with self.assertRaisesRegex(ValueError, "expected deployed QML plugin is missing"):
                    MODULE.relocate_qml_plugins(app)
                self.assertEqual((directory / "qmldir").read_text(), original)

    def test_qml_module_plugin_directory_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            app = Path(temporary)
            plugins = app / "Contents/PlugIns"
            plugins.mkdir(parents=True)
            (plugins / "libmodelsplugin.dylib").write_bytes(b"deployed plugin")
            directory = app / "Contents/Resources/qml/QtQml/Models"
            directory.mkdir(parents=True)
            (directory / "qmldir").write_text("optional plugin modelsplugin\n")
            local = directory / "libmodelsplugin.dylib"
            local.mkdir()
            with self.assertRaisesRegex(ValueError, "unexpected QML module plugin"):
                MODULE.relocate_qml_plugins(app)
            self.assertTrue(local.is_dir())

    def test_qml_foreign_links_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            app = Path(temporary)
            (app / "Contents/PlugIns").mkdir(parents=True)
            qml = app / "Contents/Resources/qml"
            qml.mkdir(parents=True)
            (app / "external.dylib").write_bytes(b"outside")
            link = qml / "fixture.dylib"
            link.symlink_to("../../../external.dylib")
            with self.assertRaises(ValueError):
                MODULE.relocate_qml_plugins(app)
            self.assertTrue(link.is_symlink())

    def test_qml_declared_plugin_must_match_central_filename(self):
        with tempfile.TemporaryDirectory() as temporary:
            app = Path(temporary)
            plugins = app / "Contents/PlugIns"
            qml = app / "Contents/Resources/qml/QtQuick/Controls"
            plugins.mkdir(parents=True)
            qml.mkdir(parents=True)
            (plugins / "libexpectedplugin.dylib").write_bytes(b"expected")
            (plugins / "libotherplugin.dylib").write_bytes(b"other")
            (qml / "qmldir").write_text("module QtQuick.Controls\nplugin expectedplugin\n")
            (qml / "libexpectedplugin.dylib").symlink_to(
                "../../../../PlugIns/libotherplugin.dylib")
            with self.assertRaisesRegex(ValueError, "unexpected QML deployment link"):
                MODULE.relocate_qml_plugins(app)


if __name__ == "__main__":
    unittest.main()
