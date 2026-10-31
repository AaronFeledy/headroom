#pragma once
#include <QJsonValue>
#include <QVariantMap>
#include <QStringList>
#include <QDateTime>

namespace Usage {
// Whitelist the additive contract independently of the required usage fields.
QJsonValue normalizeDetails(const QJsonValue &value);
QStringList detailRows(const QVariantMap &provider,
                       const QDateTime &now = QDateTime::currentDateTimeUtc());
QStringList detailSummary(const QVariantMap &provider,
                          const QDateTime &now = QDateTime::currentDateTimeUtc());
}
