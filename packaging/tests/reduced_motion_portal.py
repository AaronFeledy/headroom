#!/usr/bin/python3
"""Settings portal test double; run ONLY inside a fresh dbus-run-session.

Usage: dbus-run-session -- /usr/bin/python3 reduced_motion_portal.py TEST [ARGS...]
Requires distro python3-dbus/python3-gi. No user GNOME settings are changed.
Qt 6.12 QGnomeTheme reads reduced-motion (uint32 1) asynchronously via ReadAll.
"""
import os
import subprocess
import sys

import dbus
import dbus.service
from dbus.mainloop.glib import DBusGMainLoop
from gi.repository import GLib


class Settings(dbus.service.Object):
    """Minimal wire-compatible org.freedesktop.portal.Settings fixture."""

    @dbus.service.method("org.freedesktop.portal.Settings", in_signature="as", out_signature="a{sa{sv}}")
    def ReadAll(self, namespaces: list[str]) -> dbus.Dictionary:
        return dbus.Dictionary({"org.freedesktop.appearance": dbus.Dictionary(
            {"reduced-motion": dbus.UInt32(1, variant_level=1)}, signature="sv")}, signature="sa{sv}")

    @dbus.service.method("org.freedesktop.portal.Settings", in_signature="ss", out_signature="v")
    def Read(self, namespace: str, key: str) -> dbus.UInt32:
        if (namespace, key) != ("org.freedesktop.appearance", "reduced-motion"):
            raise dbus.exceptions.DBusException(name="org.freedesktop.portal.Error.NotFound")
        return dbus.UInt32(1, variant_level=1)

    @dbus.service.signal("org.freedesktop.portal.Settings", signature="ssv")
    def SettingChanged(self, namespace: str, key: str, value: dbus.UInt32) -> None:
        """The fixture is constant; Qt can subscribe using the real signature."""


def main() -> int:
    DBusGMainLoop(set_as_default=True)
    bus = dbus.SessionBus()
    name = dbus.service.BusName("org.freedesktop.portal.Desktop", bus=bus, do_not_queue=True)
    settings = Settings(name, "/org/freedesktop/portal/desktop")
    loop = GLib.MainLoop()
    env = dict(os.environ, QT_QPA_PLATFORM="xcb", QT_QPA_PLATFORMTHEME="gnome",
               QT_QUICK_BACKEND="software", HEADROOM_EXPECT_PLATFORM_REDUCED_MOTION="1")
    with subprocess.Popen(sys.argv[1:], env=env) as child:
        def completed() -> bool:
            if child.poll() is None:
                return True
            loop.quit()
            return False
        GLib.timeout_add(50, completed)
        try:
            loop.run()
            return child.wait()
        finally:
            if child.poll() is None:
                child.terminate()
            settings.remove_from_connection()


if __name__ == "__main__":
    sys.exit(main())
