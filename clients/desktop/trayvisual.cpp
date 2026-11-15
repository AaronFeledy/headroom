#include "trayvisual.h"
#include "usage.h"
#include <cmath>

namespace {
QString resetDescription(const QVariantMap &bucket, const QDateTime &now) {
    const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs).toUTC();
    if (!reset.isValid()) return "reset time unavailable";
    const qint64 seconds = now.secsTo(reset);
    if (seconds <= 0) return "awaiting reset update";
    return "Resets in " + Usage::remainingText(seconds);
}
Usage::WarningLevel level(const QVariantMap &assessment) {
    return Usage::WarningLevel(qBound(0, assessment["severity"].toInt(), 3));
}
bool isMeter(TrayVisual::Kind kind) {
    return kind == TrayVisual::Kind::Usage || kind == TrayVisual::Kind::Exhausted;
}
QString statusText(TrayVisual::Kind kind) {
    using K = TrayVisual::Kind;
    switch (kind) {
    case K::Setup: return "Connect a backend in Settings";
    case K::Connecting: return "Connecting to backend";
    case K::Idle: return "No usage meter available for the selected provider";
    case K::Offline: return "Backend offline";
    case K::AuthError: return "Update the backend token in Settings";
    case K::ApiError: return "Backend HTTP error";
    case K::Malformed: return "Unexpected backend response";
    case K::ProviderError: return "Provider unavailable · open for details";
    case K::Exhausted: return "Selected meter exhausted";
    case K::Usage: return {};
    }
    return {};
}
}

TrayVisual::Model TrayVisual::build(const QVariantMap &state, const QVariantList &providers, const QString &shown,
                                  const QString &meter, const Assessment &assessment, const QDateTime &now) {
    Model result;
    result.provider = shown;
    result.kind = Kind::Idle;
    QString resetText, label;
    for (const auto &entry : providers) {
        const auto provider = entry.toMap();
        const QString name = provider["provider_name"].toString();
        const bool isShown = name == shown;
        const QString type = Usage::providerType(provider);
        if (isShown) result.providerType = type.toLower();
        if (!provider["is_success"].toBool()) {
            if (isShown) result.kind = Kind::ProviderError;
            continue;
        }
        const auto buckets = provider["buckets"].toList();
        int primary = -1;
        for (int i = 0; isShown && i < buckets.size(); ++i) {
            const auto bucket = buckets[i].toMap();
            if (!Usage::isPercent(bucket)) continue;
            if (primary < 0) primary = i;
            if (!meter.isEmpty() && bucket["id"].toString() == meter) { primary = i; break; }
        }
        bool metered = false, critical = false;
        for (int i = 0; i < buckets.size(); ++i) {
            const auto bucket = buckets[i].toMap();
            if (!Usage::isPercent(bucket)) continue;
            const double used = bucket["utilization"].toDouble();
            const auto severity = level(assessment(name, bucket));
            metered = true;
            critical = critical || severity == Usage::WarningLevel::Critical;
            if (!isShown) continue;
            if (i == primary) {
                result.used = used; result.level = severity;
                label = bucket["label"].toString();
                const auto pace = Usage::pacing(type, bucket, now);
                result.expected = pace["available"].toBool() ? pace["expected"].toDouble() : -1;
                result.kind = used >= 100 ? Kind::Exhausted : Kind::Usage;
                resetText = resetDescription(bucket, now);
            } else if (int(severity) > int(result.secondary)) result.secondary = severity;
        }
        if (metered) result.critical.insert(name, critical);
    }
    const auto status = state["status"].toString();
    if (status == "offline") {
        const auto error = state["errorKind"].toString();
        result.kind = error == "auth" ? Kind::AuthError : error == "malformed" ? Kind::Malformed
            : error == "api" ? Kind::ApiError : Kind::Offline;
    } else if (status == "connecting" || (state["loading"].toBool() && !state["lastGood"].toLongLong())) result.kind = Kind::Connecting;
    else if (status == "setup") result.kind = Kind::Setup;
    const QString name = shown.isEmpty() ? "Headroom" : Usage::displayName(shown);
    if (isMeter(result.kind)) {
        const QString meterName = label.isEmpty() ? name : name + " · " + label;
        result.tooltip = QString("%1 · %2% used\n%3").arg(meterName).arg(qRound(result.used)).arg(resetText);
        const auto highest = Usage::WarningLevel(qMax(int(result.level), int(result.secondary)));
        if (highest != Usage::WarningLevel::Normal) result.tooltip += " · " + Usage::warningName(highest);
    } else result.tooltip = name + "\n" + statusText(result.kind);
    return result;
}

TrayVisual::Selection TrayVisual::constrainedProvider(const QVariantList &providers, const QString &incumbent,
                                                      const Assessment &assessment, const QDateTime &now) {
    struct Score { int tier = -1; double used = 0; QString meter; };
    const auto score = [&](const QVariantMap &provider) {
        Score best;
        if (!provider["is_success"].toBool()) return best;
        const QString name = provider["provider_name"].toString();
        for (const auto &entry : provider["buckets"].toList()) {
            const auto bucket = entry.toMap();
            bool valid = false;
            const double used = bucket["utilization"].toDouble(&valid);
            if (!Usage::isPercent(bucket) || !valid || !std::isfinite(used)) continue;
            // A passed window keeps its last tier until the replacement arrives;
            // it must not hold the tray against current readings.
            const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs);
            if (reset.isValid() && reset <= now) continue;
            const int tier = int(level(assessment(name, bucket)));
            if (tier > best.tier || (tier == best.tier && used > best.used)) best = {tier, used, bucket["id"].toString()};
        }
        return best;
    };
    Selection chosen;
    Score top, held;
    for (const auto &entry : providers) {
        const auto provider = entry.toMap();
        const auto current = score(provider);
        if (current.tier < 0) continue;
        const QString key = Usage::rowKey(provider);
        if (key == incumbent) held = current;
        // Strictly greater: ties keep the provider that comes first in the order.
        if (current.tier > top.tier || (current.tier == top.tier && current.used > top.used)) {
            chosen = {key, current.meter}; top = current;
        }
    }
    if (held.tier < 0 || chosen.key == incumbent) return chosen;
    if (top.tier > held.tier || top.used >= held.used + SwitchMargin) return chosen;
    return {incumbent, held.meter};
}
