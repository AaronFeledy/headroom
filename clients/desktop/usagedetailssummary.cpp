#include "usagedetails.h"
#include "usage.h"

namespace {
bool visible(const QVariantMap &section) {
    return section["state"] == "ok" || section["state"] == "stale";
}
QString historical(const QVariantMap &section, const QString &text) {
    return section["state"] == "stale" ? QStringLiteral("Previously reported: ") + text : text;
}
QString dollars(const QVariant &value) {
    const qint64 cents = value.toLongLong();
    return QStringLiteral("$%1.%2 USD").arg(cents / 100).arg(cents % 100, 2, 10, QLatin1Char('0'));
}
}

QStringList Usage::detailSummary(const QVariantMap &provider, const QDateTime &now) {
    QStringList rows;
    if (!provider["error"].isNull() || !provider["is_success"].toBool()) return rows;
    const auto details = provider["usage_details"].toMap();
    for (const auto &value : details["quotas"].toList()) {
        const auto quota = value.toMap();
        if (!visible(quota) || quota["bucket_id"] != "weekly_grok_bot" || provider["provider_name"] != "Cursor") continue;
        QStringList facts;
        if (!quota["has_available_usage"].isNull())
            facts << (quota["has_available_usage"].toBool() ? QStringLiteral("Usage available") : QStringLiteral("No usage available"));
        const auto scope = quota["scope"].toMap();
        if (scope["shared_pool"].toBool()) facts << QStringLiteral("Shared allowance");
        if (!facts.isEmpty()) rows << historical(quota, QStringLiteral("Grok Bot: ") + facts.join(QStringLiteral(" / ")));
    }
    const auto finances = details["finances"].toMap();
    if (visible(finances)) {
        for (const auto &value : finances["items"].toList()) {
            const auto item = value.toMap();
            if (item["prepaid_remaining_minor"].isNull()) continue;
            if (item["prepaid_remaining_minor"].toLongLong() == 0 && !item["extra_enabled"].toBool()) continue;
            QString text = QStringLiteral("Prepaid balance: ") + dollars(item["prepaid_remaining_minor"]);
            if (item["scope"].toMap()["kind"] == "team") text += QStringLiteral(" (team)");
            rows << historical(finances, text);
        }
    }
    const auto grants = details["credit_grants"].toMap();
    qsizetype grantCount = 0;
    for (const auto &value : grants["items"].toList()) {
        const auto item = value.toMap();
        if (item["remaining_minor"].toLongLong() > 0 || item["total_minor"].toLongLong() > 0) ++grantCount;
    }
    if (visible(grants) && grantCount > 0)
        rows << historical(grants, grantCount == 1 ? QStringLiteral("1 restricted grant reported")
            : QStringLiteral("%1 restricted grants reported").arg(grantCount));
    const auto inventory = details["reset_inventory"].toMap();
    if (provider["provider_name"] == "Grok" && visible(inventory) && inventory["reported_count"].toLongLong() > 0) {
        const qint64 count = inventory["reported_count"].toLongLong();
        QString text = QStringLiteral("Reset inventory (read-only): %1 reported").arg(count);
        QDateTime next;
        for (const auto &value : inventory["windows"].toList()) {
            const auto expiry = QDateTime::fromString(value.toMap()["expires_at"].toString(), Qt::ISODateWithMs);
            if (expiry.isValid() && expiry > now && (!next.isValid() || expiry < next)) next = expiry;
        }
        if (next.isValid()) text += QStringLiteral(" / Next expiry ") + Usage::resetTimeLabel(next.toString(Qt::ISODateWithMs));
        rows << historical(inventory, text);
    }
    return rows;
}
