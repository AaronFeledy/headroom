#include "displayplatform.h"

QByteArray DesktopPlatform::preferredPlatform(const QProcessEnvironment &environment, bool hasLayerShell)
{
    if (!environment.value(QStringLiteral("QT_QPA_PLATFORM")).isEmpty() || hasLayerShell)
        return {};
    const bool waylandSession = environment.value(QStringLiteral("XDG_SESSION_TYPE")) == QStringLiteral("wayland")
        || !environment.value(QStringLiteral("WAYLAND_DISPLAY")).isEmpty();
    if (!waylandSession || environment.value(QStringLiteral("DISPLAY")).isEmpty()) return {};
    // A regular Wayland top-level cannot be positioned beside the tray. Builds
    // without LayerShellQt use XWayland for placement, retaining Wayland as a
    // fallback if the X display is unavailable. Explicit Qt overrides win.
    return QByteArrayLiteral("xcb;wayland");
}

void DesktopPlatform::configure()
{
#ifdef Q_OS_LINUX
#ifdef HEADROOM_LAYER_SHELL
    constexpr bool hasLayerShell = true;
#else
    constexpr bool hasLayerShell = false;
#endif
    const auto platform = preferredPlatform(QProcessEnvironment::systemEnvironment(), hasLayerShell);
    if (!platform.isEmpty()) qputenv("QT_QPA_PLATFORM", platform);
#endif
}
