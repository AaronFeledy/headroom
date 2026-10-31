#pragma once
#include <QByteArray>
#include <QJsonObject>
#include <QString>

namespace TestUsage {
QJsonObject richDetails(const QString &state = QString::fromLatin1("ok"));
QByteArray richSnapshot(const QString &state = QString::fromLatin1("ok"));
}
