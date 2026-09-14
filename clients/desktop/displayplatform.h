#pragma once
#include <QByteArray>
#include <QProcessEnvironment>

namespace DesktopPlatform {
QByteArray preferredPlatform(const QProcessEnvironment &environment, bool hasLayerShell);
void configure();
}
