#include "usage.h"
#include "sshnetwork.h"
#include <QDateTime>
#include <QTimeZone>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSet>
#include <QRegularExpression>
#include <cmath>

QString Usage::displayName(const QString &provider) {
    return provider.compare("Codex", Qt::CaseInsensitive) == 0 ? QString("ChatGPT") : provider;
}

QUrl Usage::endpoint(const QString &base) {
    QUrl ssh;
    if (SshTransport::parseAddress(base.trimmed(), &ssh)) {
        ssh.setPath(QStringLiteral("/api/v1/usage"));
        return ssh;
    }
    const QUrl url(base.trimmed(), QUrl::StrictMode);
    if (!url.isValid() || (url.scheme() != "http" && url.scheme() != "https") || url.host().isEmpty()
        || !url.userInfo().isEmpty() || url.hasQuery() || url.hasFragment()) return {};
    QUrl result(url);
    result.setPath(url.path().remove(QRegularExpression("/+$")) + "/api/v1/usage");
    return result;
}

bool Usage::parse(const QByteArray &json, QVariantList &providers) {
    QJsonParseError error;
    const auto doc = QJsonDocument::fromJson(json, &error);
    if (error.error != QJsonParseError::NoError || !doc.isArray() || doc.array().size() > 64) return false;
    QVariantList result;
    QSet<QString> names;
    for (const auto &value : doc.array()) {
        if (!value.isObject()) return false;
        auto p = value.toObject();
        const auto name = p["provider_name"].toString().trimmed();
        if (name.isEmpty() || names.contains(name.toLower()) || !p.contains("error")
            || (!p["error"].isNull() && !p["error"].isString())
            || !p["is_success"].isBool() || p["is_success"].toBool() != p["error"].isNull()
            || !p["needs_reauth"].isBool()) return false;
        names.insert(name.toLower());
        QJsonValue resetCredits(QJsonValue::Null);
        if (p["error"].isNull() && p["rate_limit_reset_credits"].isObject()) {
            const auto credits = p["rate_limit_reset_credits"].toObject();
            const auto countValue = credits["available_count"];
            const double count = countValue.toDouble(-1);
            constexpr double maxSafeInteger = 9007199254740991.0;
            if (countValue.isDouble() && std::isfinite(count) && count >= 0
                && count <= maxSafeInteger && std::floor(count) == count) {
                const QString fingerprint = credits["account_fingerprint"].toString();
                static const QRegularExpression fingerprintPattern(QStringLiteral("^[0-9a-f]{64}$"));
                resetCredits = QJsonObject{{"available_count", count},
                    {"account_fingerprint", fingerprint.size() == 64 && fingerprintPattern.match(fingerprint).hasMatch()
                        ? QJsonValue(fingerprint) : QJsonValue(QJsonValue::Null)}};
            }
        }
        p["rate_limit_reset_credits"] = resetCredits;
        QJsonArray buckets;
        if (p["error"].isNull()) {
            if (p.contains("buckets") && !p["buckets"].isArray()) return false;
            buckets = p.value("buckets").toArray();
            // An explicit empty list means no meters; only older servers
            // that omit the bucket contract need the legacy header fallback.
            if (!p.contains("buckets")) {
                if (!p["current"].isObject() || !p["show_secondary"].isBool()) return false;
                auto primary = p["current"].toObject();
                primary["id"] = "session"; primary["label"] = p["primary_label"];
                primary["status_text"] = p["primary_status_text"];
                buckets.append(primary);
                if (p["show_secondary"].toBool()) {
                    auto weekly = p["weekly"].toObject();
                    weekly["id"] = "weekly"; weekly["label"] = p["secondary_label"];
                    weekly["status_text"] = p["secondary_status_text"];
                    buckets.append(weekly);
                }
            }
        }
        if (buckets.size() > 12) return false;
        QSet<QString> ids;
        for (const auto &item : buckets) {
            if (!item.isObject()) return false;
            const auto b = item.toObject();
            const double utilization = b["utilization"].toDouble(-1);
            const QString id = b["id"].toString();
            if (id.isEmpty() || ids.contains(id) || b["label"].toString().trimmed().isEmpty()
                || !b["utilization"].isDouble() || !std::isfinite(utilization) || utilization < 0 || utilization > 100) return false;
            if (!b["resets_at"].isNull() && !b["resets_at"].isUndefined()
                && (!b["resets_at"].isString() || !QDateTime::fromString(b["resets_at"].toString(), Qt::ISODateWithMs).isValid())) return false;
            if (!b["status_text"].isUndefined() && !b["status_text"].isNull() && !b["status_text"].isString()) return false;
            ids.insert(id);
        }
        // Match Windows' presentation fallback for older API responses. Auto
        // keeps its countdown; Other Models uses the primary billing status.
        for (int i = 0; i < buckets.size(); ++i) {
            auto b = buckets[i].toObject();
            if (b["status_text"].toString().trimmed().isEmpty()) {
                const auto id = b["id"].toString().toLower();
                if ((id == "api" || (i == 0 && id != "auto")) && !p["primary_status_text"].toString().trimmed().isEmpty())
                    b["status_text"] = p["primary_status_text"];
                else if ((id == "weekly" || id == "on_demand") && !p["secondary_status_text"].toString().trimmed().isEmpty())
                    b["status_text"] = p["secondary_status_text"];
            }
            // Optional metadata never invalidates an otherwise usable snapshot.
            const auto start = QDateTime::fromString(b["starts_at"].toString(), Qt::ISODateWithMs);
            const auto end = QDateTime::fromString(b["resets_at"].toString(), Qt::ISODateWithMs);
            const auto span = start.secsTo(end);
            if (!start.isValid() || !end.isValid() || span <= 0 || span > 366LL * 86400)
                b["starts_at"] = QJsonValue::Null;
            if (!b["detail_text"].isString() || b["detail_text"].toString().size() > 4096)
                b["detail_text"] = QJsonValue::Null;
            buckets[i] = b;
        }
        p["buckets"] = buckets;
        p["provider_name"] = name;
        p["display_name"] = displayName(name);
        result.append(p.toVariantMap());
    }
    const QStringList order {"claude", "codex", "cursor", "grok"};
    std::stable_sort(result.begin(), result.end(), [&](const QVariant &a, const QVariant &b) {
        auto rank = [&](const QVariant &v) { auto i = order.indexOf(v.toMap()["provider_name"].toString().toLower()); return i < 0 ? 99 : i; };
        return rank(a) < rank(b);
    });
    providers = result;
    return true;
}

QString Usage::countdown(const QString &timestamp) {
    const auto when = QDateTime::fromString(timestamp, Qt::ISODateWithMs);
    if (!when.isValid()) return "No scheduled reset";
    const auto seconds = QDateTime::currentDateTimeUtc().secsTo(when);
    if (seconds <= 0) return "Reset pending";
    const auto minutes = (seconds + 59) / 60;
    if (minutes >= 1440) return QString("Resets in %1d %2h").arg(minutes / 1440).arg((minutes % 1440) / 60);
    if (minutes >= 60) return QString("Resets in %1h %2m").arg(minutes / 60).arg(minutes % 60);
    return QString("Resets in %1m").arg(minutes);
}

QString Usage::resetTimeLabel(const QString &timestamp, const QDateTime &now, const QLocale &locale) {
    const auto when = QDateTime::fromString(timestamp, Qt::ISODateWithMs);
    if (!when.isValid() || !now.isValid()) return {};
    const auto local = when.toTimeZone(now.timeZone());
    const auto date = local.date();
    const auto days = now.date().daysTo(date);
    QString day;
    if (days == 0) day = QStringLiteral("Today");
    else if (days == 1) day = QStringLiteral("Tomorrow");
    // Count local calendar dates, not elapsed 24-hour periods. Next Monday
    // must show a date on Monday even when it is only 6 days and 5 hours away.
    else if (days > 1 && days < 7) day = locale.dayName(date.dayOfWeek(), QLocale::LongFormat);
    else day = locale.toString(date, date.year() == now.date().year()
        ? QStringLiteral("ddd, MMM d") : QStringLiteral("ddd, MMM d, yyyy"));
    return QStringLiteral("%1 at %2").arg(day, locale.toString(local.time(), QLocale::ShortFormat));
}

QVariantMap Usage::period(const QString &provider, const QVariantMap &bucket) {
    const QString name = provider.toLower(), id = bucket["id"].toString().toLower();
    const bool knownProvider = QStringList{"claude", "codex", "cursor", "grok"}.contains(name);
    qint64 duration = 0, step = 0;
    QString window, unit;
    if (knownProvider && (id == "weekly" || id.startsWith("weekly_"))) {
        duration = 7 * 86400; step = 86400; window = "7-day window"; unit = "Day";
    } else if ((name == "claude" || name == "codex") && id == "session") {
        const bool weeklyFallback = bucket["label"].toString().contains("weekly", Qt::CaseInsensitive);
        duration = weeklyFallback ? 7 * 86400 : 5 * 3600;
        step = weeklyFallback ? 86400 : 3600;
        window = weeklyFallback ? "7-day window" : "5-hour window";
        unit = weeklyFallback ? "Day" : "Hour";
    } else if (name == "cursor" && QStringList{"session", "plan", "auto", "api", "on_demand"}.contains(id)) {
        duration = 30 * 86400; step = 7 * 86400; window = "30-day billing estimate"; unit = "Week";
    } else if (name == "grok" && QStringList{"session", "credits", "plan", "on_demand"}.contains(id)) {
        const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs).toUTC();
        if (reset.isValid()) duration = reset.addMonths(-1).secsTo(reset);
        step = 7 * 86400; window = "calendar-month billing estimate"; unit = "Week";
    }
    const auto start = QDateTime::fromString(bucket["starts_at"].toString(), Qt::ISODateWithMs).toUTC();
    const auto end = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs).toUTC();
    const qint64 reportedDuration = start.secsTo(end);
    if (start.isValid() && end.isValid() && reportedDuration > 0 && reportedDuration <= 366LL * 86400) {
        duration = reportedDuration;
        step = duration > 14 * 86400 ? 7 * 86400 : duration > 86400 ? 86400 : 3600;
        unit = step == 7 * 86400 ? "Week" : step == 86400 ? "Day" : "Hour";
        window = "provider-reported usage window";
    }
    return {{"seconds", duration}, {"step", step}, {"unit", unit}, {"label", window}};
}

QVariantList Usage::notches(const QString &provider, const QVariantMap &bucket) {
    const auto window = period(provider, bucket);
    const auto duration = window["seconds"].toLongLong(), step = window["step"].toLongLong();
    QVariantList result;
    if (duration <= 0 || step <= 0) return result;
    for (qint64 elapsed = step; elapsed < duration; elapsed += step) {
        const auto label = QString("%1 %2").arg(window["unit"].toString()).arg(elapsed / step);
        result.append(QVariantMap{{"fraction", double(elapsed) / duration},
            {"label", label + (window["unit"] == "Week" ? QString(" · day %1").arg(elapsed / 86400) : QString())}});
    }
    return result;
}

QVariantMap Usage::pacing(const QString &provider, const QVariantMap &bucket, const QDateTime &now) {
    auto unavailable = [](const QString &reason) {
        return QVariantMap{{"available", false}, {"label", "Pace unavailable"}, {"detail", reason}};
    };
    const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs).toUTC();
    if (!reset.isValid()) return unavailable("This meter has no reset time, so its pacing cannot be estimated.");
    if (reset <= now) return unavailable("The reset time has passed. Waiting for the backend's next usage window.");
    const auto periodInfo = period(provider, bucket);
    const auto duration = periodInfo["seconds"].toLongLong();
    const auto window = periodInfo["label"].toString();
    const auto id = bucket["id"].toString().toLower();
    if (duration <= 0) return unavailable("This meter has no known usage-window length. A reset time alone cannot establish pacing.");
    const QString status = bucket["status_text"].toString();
    if (id == "on_demand" && bucket["utilization"].toDouble() <= 0 && !status.isEmpty() && !status.contains(" / "))
        return unavailable("This meter reports billing status without a measured usage percentage.");
    const auto remaining = now.secsTo(reset);
    if (remaining > duration) return unavailable("The reset is outside the expected usage window; no pacing estimate is shown.");
    const double expected = 100.0 * (duration - remaining) / duration;
    const double used = bucket["utilization"].toDouble();
    const double difference = used - expected;
    const qint64 seconds = qRound64(difference * duration / 100.0);
    const bool onPace = std::abs(seconds) < 60;
    const qint64 minutes = (std::abs(seconds) + 30) / 60;
    QString offset;
    if (minutes >= 1440) {
        offset = QString("%1d").arg(minutes / 1440);
        if ((minutes % 1440) / 60) offset += QString(" %1h").arg((minutes % 1440) / 60);
    } else if (minutes >= 60) {
        offset = QString("%1h").arg(minutes / 60);
        if (minutes % 60) offset += QString(" %1m").arg(minutes % 60);
    } else offset = QString("%1m").arg(minutes);
    const QString label = onPace ? "On pace" : QString("%1 %2 pace").arg(offset, difference > 0 ? "ahead of" : "behind");
    const QString explanation = onPace ? "Your usage is within one minute of steady spending."
        : difference > 0 ? QString("You've used the allowance scheduled for %1 from now.").arg(offset)
                        : QString("You have %1 of steady-spending allowance in reserve.").arg(offset);
    const QString detail = QString("%1% used · %2% expected by now · %3 pp %4 pace.\n%5\nThe marker estimates steady spending across a %6.\nThis compares usage with elapsed time; it does not predict when you'll run out.")
        .arg(used, 0, 'f', 1).arg(expected, 0, 'f', 1).arg(std::abs(difference), 0, 'f', 1)
        .arg(onPace ? "from" : difference >= 0 ? "over" : "under").arg(explanation, window);
    return {{"available", true}, {"expected", expected}, {"difference", difference},
        {"onPace", onPace}, {"over", !onPace && difference > 0}, {"label", label}, {"detail", detail}};
}

QVariantMap Usage::concern(const QString &provider, const QVariantMap &bucket, const QDateTime &now) {
    const auto pace = pacing(provider, bucket, now);
    const double used = qBound(0.0, bucket["utilization"].toDouble(), 100.0);
    const double remaining = 100.0 - used;
    const bool available = pace["available"].toBool();
    double pressure = 0;
    QString explanation;
    if (available) {
        const double timeRemaining = 100.0 - pace["expected"].toDouble();
        // Fraction of the allowance for the remaining time already spent early.
        // Divide by remaining time, not elapsed time: small early bursts stay calm.
        pressure = qBound(0.0, (timeRemaining - remaining) / qMax(0.000001, timeRemaining), 1.0);
        explanation = QString("%1% allowance left · %2% of the window left.\n%3% of the expected remaining allowance has been spent ahead of schedule.")
            .arg(remaining, 0, 'f', 1).arg(timeRemaining, 0, 'f', 1).arg(pressure * 100, 0, 'f', 1);
        if (used >= 95)
            explanation += used >= 100 ? "\nThe allowance is exhausted." : "\nVery little allowance remains, even if usage is on pace.";
        explanation += "\nPacing concern: yellow at 10%, orange at 25%, red at 50% of remaining allowance spent early.";
    } else {
        // Retain the original Windows percentage fallback only without timing.
        explanation = "Window timing is unavailable. Color uses usage alone: yellow at 50%, orange at 75%, red at 90%.";
    }
    const auto level = warningLevel(used, available, pressure);
    return {{"severity", int(level)}, {"color", warningColor(level)}, {"level", warningName(level)}, {"pressure", pressure}, {"available", available},
        {"remaining", remaining}, {"detail", pace["detail"].toString() + "\n\n" + explanation}};
}
