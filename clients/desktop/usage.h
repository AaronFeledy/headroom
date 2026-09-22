#pragma once
#include "warning.h"
#include <QByteArray>
#include <QVariantList>
#include <QUrl>
#include <QDateTime>
#include <QLocale>
#include <QVariantMap>
namespace Usage {
bool parse(const QByteArray &json, QVariantList &providers);
QUrl endpoint(const QString &base);
QString countdown(const QString &timestamp);
QString resetTimeLabel(const QString &timestamp,
                       const QDateTime &now = QDateTime::currentDateTime(),
                       const QLocale &locale = QLocale());
QVariantMap period(const QString &provider, const QVariantMap &bucket);
QVariantList notches(const QString &provider, const QVariantMap &bucket);
QVariantMap pacing(const QString &provider, const QVariantMap &bucket,
                   const QDateTime &now = QDateTime::currentDateTimeUtc());
// 0 normal, 1 watch (yellow), 2 warning (orange), 3 critical (red).
QVariantMap concern(const QString &provider, const QVariantMap &bucket,
                    const QDateTime &now = QDateTime::currentDateTimeUtc());
QString displayName(const QString &provider);
}
