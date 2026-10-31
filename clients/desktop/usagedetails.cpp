#include "usagedetails.h"
#include "usage.h"
#include <QJsonArray>
#include <QJsonObject>
#include <QRegularExpression>
#include <cmath>

namespace {
const QJsonValue nullValue(QJsonValue::Null);
QJsonValue label(const QJsonValue &v) {
    if (!v.isString()) return nullValue;
    const QString s = v.toString().trimmed();
    // Display labels only: never render markup, addresses, URLs or identifiers.
    static const QRegularExpression unsafe(QStringLiteral("[<>@:/\\\\\\x{0000}-\\x{001f}\\x{007f}-\\x{009f}\\x{202a}-\\x{202e}\\x{2066}-\\x{2069}]|[0-9a-fA-F]{32}"));
    return s.isEmpty() || s.size() > 80 || unsafe.match(s).hasMatch() ? nullValue : QJsonValue(s);
}
QJsonValue timeValue(const QJsonValue &v) {
    if (!v.isString() || v.toString().size() > 64) return nullValue;
    static const QRegularExpression zone(QStringLiteral("(Z|[+-][0-9]{2}:[0-9]{2})$"));
    const auto t = QDateTime::fromString(v.toString(), Qt::ISODateWithMs);
    return !t.isValid() || t.date().year() < 1970 || t.date().year() > 9999
        || !zone.match(v.toString()).hasMatch() ? nullValue : QJsonValue(t.toUTC().toString(Qt::ISODateWithMs));
}
QJsonValue integer(const QJsonValue &v) {
    const double n = v.toDouble(-1);
    return !v.isDouble() || !std::isfinite(n) || n < 0 || n > 9007199254740991.0 || std::floor(n) != n
        ? nullValue : v;
}
QJsonValue boolean(const QJsonValue &v) { return v.isBool() ? v : nullValue; }
QJsonValue scope(const QJsonValue &v) {
    if (!v.isObject()) return nullValue;
    const auto o = v.toObject();
    const QString kind = o["kind"].toString();
    return QJsonObject{{"kind", QStringList{"personal", "team"}.contains(kind) ? kind : QStringLiteral("unknown")},
        {"shared_pool", boolean(o["shared_pool"])}};
}
QJsonObject observation(const QJsonValue &v) {
    if (!v.isObject()) return {};
    const auto o = v.toObject();
    const QString state = o["state"].toString();
    if (!QStringList{"ok", "stale", "unknown", "unsupported"}.contains(state)) return {};
    return {{"state", state}, {"observed_at", timeValue(o["observed_at"])}};
}
template <typename Decode> QJsonValue list(const QJsonValue &v, Decode decode) {
    if (!v.isArray() || v.toArray().size() > 64) return nullValue;
    QJsonArray result;
    for (const auto &item : v.toArray()) {
        if (!item.isObject()) continue;
        const auto row = decode(item.toObject());
        if (!row.isEmpty()) result.append(row);
    }
    return result;
}
QString scopeText(const QVariantMap &row) {
    const auto s = row["scope"].toMap();
    if (s["kind"] == "personal") return QStringLiteral("Personal");
    if (s["kind"] != "team") return {};
    if (s["shared_pool"].isNull()) return QStringLiteral("Team");
    return s["shared_pool"].toBool() ? QStringLiteral("Team shared pool") : QStringLiteral("Team · separate allowance");
}
bool known(const QVariantMap &s) { return s["state"] == "ok" || s["state"] == "stale"; }
QString money(const QVariant &v) {
    if (v.isNull()) return QStringLiteral("unknown");
    const qint64 n = v.toLongLong();
    return QStringLiteral("$%1.%2 USD").arg(n / 100).arg(n % 100, 2, 10, QLatin1Char('0'));
}
QString date(const QVariant &v) { return Usage::resetTimeLabel(v.toString()); }
}

QJsonValue Usage::normalizeDetails(const QJsonValue &value) {
    if (!value.isObject()) return nullValue;
    const auto input = value.toObject();
    QJsonObject out;
    auto plan = observation(input["plan"]);
    if (!plan.isEmpty()) {
        const auto p = input["plan"].toObject();
        plan["label"] = label(p["label"]); plan["is_unlimited"] = boolean(p["is_unlimited"]); plan["scope"] = scope(p["scope"]);
    }
    out["plan"] = plan.isEmpty() ? nullValue : QJsonValue(plan);
    out["quotas"] = list(input["quotas"], [](const QJsonObject &q) {
        auto row = observation(q);
        static const QRegularExpression id(QStringLiteral("^[a-z][a-z0-9_]{0,79}$"));
        if (row.isEmpty() || !id.match(q["bucket_id"].toString()).hasMatch()) return QJsonObject{};
        row["bucket_id"] = q["bucket_id"]; row["plan_label"] = label(q["plan_label"]);
        row["has_available_usage"] = boolean(q["has_available_usage"]); row["scope"] = scope(q["scope"]);
        return row;
    });
    for (const auto &key : {"finances", "credit_grants", "reset_inventory", "product_usage"}) {
        const auto raw = input[key].toObject();
        auto section = observation(input[key]);
        if (section.isEmpty()) { out[key] = nullValue; continue; }
        const QString name = QLatin1String(key);
        if (name == "finances") section["items"] = list(raw["items"], [](const QJsonObject &r) {
            if (r["currency"] != "USD") return QJsonObject{};
            QJsonObject o{{"currency", "USD"}, {"scope", scope(r["scope"])}, {"starts_at", timeValue(r["starts_at"])}, {"resets_at", timeValue(r["resets_at"])}};
            for (const auto &k : {"included_used_minor", "included_limit_minor", "included_remaining_minor", "prepaid_remaining_minor", "extra_used_minor", "extra_cap_minor"}) o[k] = integer(r[k]);
            o["extra_enabled"] = boolean(r["extra_enabled"]); o["auto_top_up_enabled"] = boolean(r["auto_top_up_enabled"]);
            return o;
        });
        if (name == "credit_grants") section["items"] = list(raw["items"], [](const QJsonObject &r) {
            if (r["currency"] != "USD") return QJsonObject{};
            return QJsonObject{{"currency", "USD"}, {"display_name", label(r["display_name"])}, {"product", label(r["product"])},
                {"remaining_minor", integer(r["remaining_minor"])}, {"total_minor", integer(r["total_minor"])},
                {"expires_at", timeValue(r["expires_at"])}, {"scope", scope(r["scope"])}};
        });
        if (name == "reset_inventory") {
            section["reported_count"] = integer(raw["reported_count"]); section["valid_now_count"] = integer(raw["valid_now_count"]);
            section["windows"] = list(raw["windows"], [](const QJsonObject &r) {
                const auto count = integer(r["count"]);
                if (count.isNull()) return QJsonObject{};
                return QJsonObject{{"count", count}, {"valid_from", timeValue(r["valid_from"])}, {"expires_at", timeValue(r["expires_at"])}};
            });
        }
        if (name == "product_usage") {
            section["parent_bucket_id"] = label(raw["parent_bucket_id"]);
            section["items"] = list(raw["items"], [](const QJsonObject &r) {
                const auto product = label(r["product"]); const double n = r["reported_percent"].toDouble(-1);
                if (product.isNull() || !r["reported_percent"].isDouble() || !std::isfinite(n) || n < 0 || n > 100) return QJsonObject{};
                return QJsonObject{{"product", product}, {"reported_percent", n}};
            });
        }
        out[key] = section;
    }
    return out;
}

QStringList Usage::detailRows(const QVariantMap &provider, const QDateTime &now) {
    QStringList rows;
    if (!provider["error"].isNull() || !provider["is_success"].toBool()) return rows;
    const auto details = provider["usage_details"].toMap();
    auto add = [&](const QVariantMap &section, const QString &text, const QVariantMap &row = QVariantMap{}) {
        if (text.isEmpty() || !known(section)) return;
        QString result = text;
        const auto context = scopeText(row);
        if (!context.isEmpty()) result += " · " + context;
        if (section["state"] == "stale") result = "Previously reported: " + result;
        const auto observed = date(section["observed_at"]);
        if (!observed.isEmpty()) result += " · Observed " + observed;
        rows.append(result);
    };
    const auto plan = details["plan"].toMap();
    QStringList planFacts;
    if (!plan["label"].toString().isEmpty() && plan["label"].toString() != provider["subtitle"].toString()) planFacts << plan["label"].toString();
    if (!plan["is_unlimited"].isNull()) planFacts << (plan["is_unlimited"].toBool() ? "Unlimited plan reported" : "Limited plan reported");
    if (!planFacts.isEmpty() || !scopeText(plan).isEmpty()) add(plan, planFacts.isEmpty() ? "Plan" : planFacts.join(" · "), plan);
    for (const auto &v : details["quotas"].toList()) {
        const auto q = v.toMap(); const QString id = q["bucket_id"].toString();
        const bool sand = id == "weekly_grok_bot" && provider["provider_name"] == "Cursor";
        QVariantMap bucket;
        for (const auto &b : provider["buckets"].toList()) if (b.toMap()["id"] == id) bucket = b.toMap();
        if (bucket.isEmpty() && !sand) continue;
        QStringList facts;
        if (!q["plan_label"].toString().isEmpty()) facts << q["plan_label"].toString();
        // Sand's availability is meaningful only on its real Cursor meter. True
        // is not evidence of a purchased allowance or shared standalone account.
        if (sand && !q["has_available_usage"].isNull())
            facts << (q["state"] == "stale" ? (q["has_available_usage"].toBool() ? "Availability was reported" : "No available usage was reported")
                : q["has_available_usage"].toBool() ? "Usage available (not proof of paid access)" : "No usage available");
        if (!facts.isEmpty() || !scopeText(q).isEmpty()) add(q, (bucket.isEmpty() ? QStringLiteral("Grok Bot") : bucket["label"].toString()) + ": " + facts.join(" · "), q);
    }
    const auto finances = details["finances"].toMap();
    for (const auto &v : finances["items"].toList()) {
        const auto r = v.toMap(); QStringList facts;
        bool meaningful = r["extra_enabled"].toBool() || r["auto_top_up_enabled"].toBool();
        for (const auto &k : {"included_used_minor", "included_limit_minor", "included_remaining_minor", "prepaid_remaining_minor", "extra_used_minor", "extra_cap_minor"}) meaningful |= r[k].toLongLong() > 0;
        if (!meaningful) continue;
        if (!r["prepaid_remaining_minor"].isNull()) facts << "Prepaid balance " + money(r["prepaid_remaining_minor"]);
        if (!r["included_used_minor"].isNull() || !r["included_limit_minor"].isNull()) facts << "Included spend " + money(r["included_used_minor"]) + " / " + money(r["included_limit_minor"]) + " cap";
        if (!r["included_remaining_minor"].isNull()) facts << "Included balance " + money(r["included_remaining_minor"]);
        if (!r["extra_used_minor"].isNull() || !r["extra_cap_minor"].isNull()) facts << "Extra spend " + money(r["extra_used_minor"]) + " / " + money(r["extra_cap_minor"]) + " cap";
        if (!r["extra_enabled"].isNull()) facts << (r["extra_enabled"].toBool() ? "Extra usage enabled" : "Extra usage disabled");
        if (!r["auto_top_up_enabled"].isNull()) facts << (r["auto_top_up_enabled"].toBool() ? "Auto top-up enabled" : "Auto top-up disabled");
        if (!date(r["starts_at"]).isEmpty()) facts << "Period starts " + date(r["starts_at"]);
        if (!date(r["resets_at"]).isEmpty()) facts << "Period ends " + date(r["resets_at"]);
        add(finances, facts.join(" · "), r);
    }
    const auto grants = details["credit_grants"].toMap();
    for (const auto &v : grants["items"].toList()) {
        const auto r = v.toMap();
        if (r["remaining_minor"].toLongLong() <= 0 && r["total_minor"].toLongLong() <= 0) continue;
        QString text = "Restricted grant: " + (r["display_name"].toString().isEmpty() ? QStringLiteral("Unnamed grant") : r["display_name"].toString());
        if (!r["product"].isNull()) text += " · Product " + r["product"].toString();
        const auto expiry = QDateTime::fromString(r["expires_at"].toString(), Qt::ISODateWithMs);
        text += expiry.isValid() && expiry <= now ? " · Expired · Reported balance " : " · Reported balance ";
        text += money(r["remaining_minor"]);
        if (!r["total_minor"].isNull()) text += " / " + money(r["total_minor"]) + " granted";
        if (expiry.isValid()) text += " · Expiry " + date(r["expires_at"]);
        add(grants, text, r);
    }
    const auto inventory = details["reset_inventory"].toMap();
    if (provider["provider_name"].toString().compare("Grok", Qt::CaseInsensitive) == 0) {
        QStringList facts;
        if (inventory["reported_count"].toLongLong() > 0) facts << inventory["reported_count"].toString() + " reported";
        if (inventory["state"] == "ok" && !inventory["valid_now_count"].isNull()
            && (inventory["reported_count"].toLongLong() > 0 || inventory["valid_now_count"].toLongLong() > 0))
            facts << inventory["valid_now_count"].toString() + " in valid windows at observation";
        if (!facts.isEmpty()) add(inventory, "Reset inventory (read-only): " + facts.join(" · "));
        for (const auto &v : inventory["windows"].toList()) {
            const auto r = v.toMap(); if (r["count"].toLongLong() <= 0) continue;
            QString text = "Reset window: " + r["count"].toString() + " reported";
            if (!date(r["valid_from"]).isEmpty()) text += " · From " + date(r["valid_from"]);
            const auto expiry = QDateTime::fromString(r["expires_at"].toString(), Qt::ISODateWithMs);
            if (expiry.isValid()) text += (expiry <= now ? " · Expired " : " · Expires ") + date(r["expires_at"]);
            add(inventory, text);
        }
    }
    const auto products = details["product_usage"].toMap();
    for (const auto &v : products["items"].toList()) {
        const auto r = v.toMap();
        add(products, "Reported contribution: " + r["product"].toString() + " · "
            + QString::number(r["reported_percent"].toDouble(), 'g', 6) + "% (not a quota)");
    }
    return rows;
}
