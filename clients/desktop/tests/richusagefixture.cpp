#include "richusagefixture.h"
#include "usagefixture.h"

namespace TestUsage {
QJsonObject richDetails(const QString &state) {
    const auto now = QDateTime::currentDateTimeUtc();
    const auto observed = now.addSecs(-120).toString(Qt::ISODate);
    const QJsonObject personal{{"kind", "personal"}, {"shared_pool", QJsonValue::Null}};
    const QJsonObject team{{"kind", "team"}, {"shared_pool", true}};
    return {{"plan", QJsonObject{{"state", state}, {"observed_at", observed}, {"label", "Pro"}, {"is_unlimited", false}, {"scope", personal}}},
        {"quotas", QJsonArray{QJsonObject{{"state", state}, {"observed_at", observed}, {"bucket_id", "weekly_grok_bot"}, {"plan_label", "Sand"}, {"has_available_usage", false}, {"scope", team}}}},
        {"finances", QJsonObject{{"state", state}, {"observed_at", observed}, {"items", QJsonArray{QJsonObject{
            {"currency", "USD"}, {"scope", personal}, {"included_used_minor", 0}, {"included_limit_minor", 2000},
            {"prepaid_remaining_minor", 1234}, {"extra_used_minor", 125}, {"extra_cap_minor", 1000},
            {"extra_enabled", true}, {"auto_top_up_enabled", false}, {"starts_at", now.addDays(-8).toString(Qt::ISODate)}, {"resets_at", now.addDays(22).toString(Qt::ISODate)}}}}}},
        {"credit_grants", QJsonObject{{"state", state}, {"observed_at", observed}, {"items", QJsonArray{
            QJsonObject{{"display_name", "Launch grant with a deliberately long descriptive cosmetic label"}, {"product", "Grok Code"}, {"currency", "USD"}, {"remaining_minor", 500}, {"total_minor", 1000}, {"expires_at", now.addDays(14).toString(Qt::ISODate)}, {"scope", personal}},
            QJsonObject{{"display_name", "Expired restricted grant"}, {"currency", "USD"}, {"remaining_minor", 250}, {"total_minor", 500}, {"expires_at", now.addDays(-1).toString(Qt::ISODate)}}}}}},
        {"reset_inventory", QJsonObject{{"state", state}, {"observed_at", observed}, {"reported_count", 3}, {"valid_now_count", 0}, {"windows", QJsonArray{
            QJsonObject{{"count", 3}, {"valid_from", now.addDays(1).toString(Qt::ISODate)}, {"expires_at", now.addDays(7).toString(Qt::ISODate)}}}}}},
        {"product_usage", QJsonObject{{"state", state}, {"observed_at", observed}, {"parent_bucket_id", "weekly"}, {"items", QJsonArray{
            QJsonObject{{"product", "Grok Code"}, {"reported_percent", 18.5}}, QJsonObject{{"product", "Chat"}, {"reported_percent", 0}}}}}}};
}
QByteArray richSnapshot(const QString &state) {
    auto providers = QJsonDocument::fromJson(snapshot()).array();
    QJsonArray result;
    for (int i : {2, 3}) {
        auto p = providers[i].toObject();
        p["usage_details"] = richDetails(state);
        p["rate_limit_reset_credits"] = QJsonObject{{"available_count", 99}};
        auto buckets = p["buckets"].toArray();
        if (i == 2) {
            auto weekly = buckets[0].toObject(); weekly["id"] = "weekly"; weekly["label"] = "Weekly";
            weekly["resets_at"] = QDateTime::currentDateTimeUtc().addDays(3).toString(Qt::ISODate);
            weekly["measurement"] = "percent"; buckets[0] = weekly;
        }
        p["buckets"] = buckets;
        result.append(p);
    }
    return QJsonDocument(result).toJson();
}
}
