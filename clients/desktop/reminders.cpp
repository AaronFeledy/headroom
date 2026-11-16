#include "reminders.h"
#include "usage.h"
#include <QCryptographicHash>
#include <QRegularExpression>
#include <cmath>
#include <utility>

namespace {
constexpr qint64 HourMs = 3600000;
constexpr qint64 DayMs = 86400000;
constexpr qint64 MatchMs = 60000;
constexpr int HourLead = 1;
constexpr int DayLead = 2;
constexpr int Depleted = 1;
constexpr int Closed = 2;

QDateTime timestamp(const QVariant &value) {
    const QString text = value.toString();
    if (text.isEmpty()) return {};
    const auto parsed = QDateTime::fromString(text, Qt::ISODateWithMs);
    return parsed.isValid() ? parsed.toUTC() : QDateTime();
}

}

Reminders::Reminders(QString path) : m_ledger(std::move(path)) {}

QList<Reminder> Reminders::evaluate(const QVariantList &providers, const QDateTime &now, bool notify, bool unused) {
    QList<Reminder> result;
    if (!now.isValid()) return result;
    const qint64 nowMs = now.toMSecsSinceEpoch();
    m_ledger.retain(nowMs);
    const auto &records = m_ledger.records();
    for (int i = 0; i < records.size(); ++i) {
        const auto &record = records[i];
        if (record.type == "session" && (record.phases & Depleted) && !(record.phases & Closed)
            && nowMs > record.boundary + HourMs) {
            m_ledger.setPhases(i, record.phases | Closed);
        }
    }
    static const QRegularExpression fingerprintPattern(QStringLiteral("\\A[0-9a-f]{64}\\z"));
    for (const auto &value : providers) {
        const auto row = value.toMap();
        if (!row.value("is_success").toBool()) continue;
        const QString name = row.value("provider_name").toString();
        if (name.isEmpty()) continue;
        const QString type = Usage::providerType(row);
        const QString display = Usage::displayName(name);
        const auto credits = row.value("rate_limit_reset_credits").toMap();
        QString fingerprint = credits.value("account_fingerprint").toString();
        if (!fingerprintPattern.match(fingerprint).hasMatch()) fingerprint.clear();
        const QString scope = QString::fromLatin1(QCryptographicHash::hash(
            (Usage::rowKey(row) + '\n' + fingerprint).toUtf8(), QCryptographicHash::Sha256).toHex().left(32));
        const QString expiryTarget = credits.contains("available_count")
            ? "bankedResets_" + name : "providerCard_" + name;

        struct Expiry { qint64 boundary; QString label; qint64 count; bool resetKind; };
        QList<Expiry> expiries;
        for (const auto &item : credits.value("kinds").toList()) {
            const auto kind = item.toMap();
            bool availableOk = false;
            const double available = kind.value("available_count").toDouble(&availableOk);
            const auto expires = timestamp(kind.value("expires_at"));
            if (!availableOk || !std::isfinite(available) || available <= 0 || !expires.isValid()
                || expires.toMSecsSinceEpoch() <= nowMs) continue;
            expiries.append({expires.toMSecsSinceEpoch(), kind.value("label").toString(), 0, true});
        }
        const auto inventory = row.value("usage_details").toMap().value("reset_inventory").toMap();
        if (inventory.value("state").toString() == "ok") {
            for (const auto &item : inventory.value("windows").toList()) {
                const auto window = item.toMap();
                const qint64 count = window.value("count").toLongLong();
                const auto from = timestamp(window.value("valid_from"));
                const auto expires = timestamp(window.value("expires_at"));
                if (count <= 0 || !from.isValid() || !expires.isValid()) continue;
                if (from.toMSecsSinceEpoch() > nowMs || expires.toMSecsSinceEpoch() <= nowMs) continue;
                expiries.append({expires.toMSecsSinceEpoch(), {}, count, false});
            }
        }
        qint64 soonest = expiries.isEmpty() ? 0 : expiries.first().boundary;
        for (const auto &candidate : expiries) soonest = qMin(soonest, candidate.boundary);
        // Records start inside the 24-hour lead; nothing is due before then.
        if (!expiries.isEmpty() && soonest - nowMs <= DayMs) {
            int index = m_ledger.locate({scope, "expiry", "", soonest});
            if (index < 0) index = m_ledger.add({scope, "expiry", "", soonest});
            const int lead = takeLead(index, nowMs);
            if (lead > 0 && notify) {
                QStringList labels;
                qint64 windowCount = 0;
                for (const auto &candidate : expiries) {
                    if (qAbs(candidate.boundary - soonest) > MatchMs) continue;
                    if (!candidate.resetKind) windowCount += candidate.count;
                    else if (!candidate.label.isEmpty() && !labels.contains(candidate.label))
                        labels.append(candidate.label);
                }
                const QString remaining = Usage::remainingText((records[index].boundary - nowMs + 999) / 1000);
                QString message;
                if (!labels.isEmpty()) {
                    message = labels.size() == 1
                        ? QStringLiteral("A banked %1 expires in %2.").arg(labels.first(), remaining)
                        : QStringLiteral("Banked resets expire in %1: %2.").arg(remaining, labels.join(", "));
                } else if (windowCount > 0) {
                    message = windowCount == 1
                        ? QStringLiteral("A reset in the %1 inventory expires in %2.").arg(display, remaining)
                        : QStringLiteral("%1 resets in the %2 inventory expire in %3.").arg(windowCount).arg(display, remaining);
                }
                if (!message.isEmpty())
                    result.append({Reminder::Kind::BankedExpiry, expiryTarget,
                        display + QStringLiteral(" · Banked reset expiring"), message, lead == 1 ? 2 : 0});
            }
        }

        for (const auto &item : row.value("buckets").toList()) {
            const auto bucket = item.toMap();
            if (bucket.value("id").toString() != "weekly" || !Usage::isPercent(bucket)) continue;
            bool ok = false;
            const double used = bucket.value("utilization").toDouble(&ok);
            if (!ok || !std::isfinite(used) || used < 0 || used > 75) continue;
            const auto reset = timestamp(bucket.value("resets_at"));
            if (!reset.isValid() || reset.toMSecsSinceEpoch() <= nowMs || reset.toMSecsSinceEpoch() - nowMs > DayMs) continue;
            const qint64 boundary = reset.toMSecsSinceEpoch();
            int index = m_ledger.locate({scope, "unused", "weekly", boundary});
            if (index < 0) index = m_ledger.add({scope, "unused", "weekly", boundary});
            const int lead = takeLead(index, nowMs);
            if (lead > 0 && unused && notify) {
                const QString label = bucket.value("label").toString();
                const QString remaining = Usage::remainingText((records[index].boundary - nowMs + 999) / 1000);
                result.append({Reminder::Kind::UnusedAllowance, "meter_" + name + "_weekly",
                    display + " · " + label + " · Unused allowance",
                    QStringLiteral("%1% of the %2 allowance is unused and resets in %3.")
                        .arg(QString::number(100 - used, 'f', 0), label, remaining), 0});
            }
        }

        struct SessionReading { QString label; double used; QDateTime reset; };
        QList<SessionReading> readings;
        for (const auto &item : row.value("buckets").toList()) {
            const auto bucket = item.toMap();
            if (bucket.value("id").toString() != "session" || !Usage::isPercent(bucket)) continue;
            bool ok = false;
            const double used = bucket.value("utilization").toDouble(&ok);
            if (!ok || !std::isfinite(used)) continue;
            const qint64 period = Usage::period(type, bucket).value("seconds").toLongLong();
            if (period <= 0 || period > 86400) continue;
            readings.append({bucket.value("label").toString(), used, timestamp(bucket.value("resets_at"))});
        }
        for (const auto &reading : readings) {
            if (reading.used < 100 || !reading.reset.isValid() || reading.reset.toMSecsSinceEpoch() <= nowMs) continue;
            const qint64 boundary = reading.reset.toMSecsSinceEpoch();
            int index = m_ledger.locate({scope, "session", "session", boundary});
            if (index < 0) index = m_ledger.add({scope, "session", "session", boundary});
            if (records[index].phases & Depleted) continue;
            m_ledger.setPhases(index, records[index].phases | Depleted);
            // One pending episode per scope and meter.
            closeOpenSessions(scope, index);
            if (!notify) continue;
            const QString remaining = Usage::remainingText((records[index].boundary - nowMs + 999) / 1000);
            result.append({Reminder::Kind::SessionDepleted, "meter_" + name + "_session",
                display + " · " + reading.label + " · Limit reached",
                QStringLiteral("The %1 allowance is used up and resets in %2.").arg(reading.label, remaining), 3});
        }
        for (const auto &reading : readings) {
            if (reading.used >= 99.5) continue;
            for (int i = 0; i < records.size(); ++i) {
                const auto &record = records[i];
                if (record.scope != scope || record.type != "session" || record.meter != "session") continue;
                if (!(record.phases & Depleted) || (record.phases & Closed)) continue;
                const bool withinWindow = nowMs <= record.boundary + HourMs;
                m_ledger.setPhases(i, record.phases | Closed);
                if (!withinWindow || !notify) continue;
                result.append({Reminder::Kind::SessionRestored, "meter_" + name + "_session",
                    display + " · " + reading.label + " · Available again",
                    QStringLiteral("The %1 allowance is available again (%2% used).")
                        .arg(reading.label, QString::number(reading.used, 'f', 0)), 0});
            }
        }
    }

    m_ledger.flush();
    return result;
}

int Reminders::takeLead(int index, qint64 nowMs) {
    const auto &record = m_ledger.records()[index];
    const qint64 remaining = record.boundary - nowMs;
    if (remaining <= 0) return 0;
    if (remaining <= HourMs && !(record.phases & HourLead)) {
        m_ledger.setPhases(index, record.phases | HourLead | DayLead);
        return 1;
    }
    if (remaining <= DayMs && !(record.phases & DayLead)) {
        m_ledger.setPhases(index, record.phases | DayLead);
        return 2;
    }
    return 0;
}

void Reminders::closeOpenSessions(const QString &scope, int except) {
    const auto &records = m_ledger.records();
    for (int i = 0; i < records.size(); ++i) {
        const auto &record = records[i];
        if (i == except || record.type != "session" || record.scope != scope || record.meter != "session") continue;
        if ((record.phases & Depleted) && !(record.phases & Closed)) {
            m_ledger.setPhases(i, record.phases | Closed);
        }
    }
}
