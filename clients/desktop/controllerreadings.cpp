#include "controller.h"
#include "usage.h"
#include <QDateTime>

namespace {
// Stale readings are a presentation aid for temporary outages, not a history.
constexpr qint64 maximumStaleReadingSecs = 24 * 3600;
constexpr qint64 transientRecoveryWindowSecs = 300;
const QString backendUnreachable = QStringLiteral("The usage server could not be reached.");
const QString connectionReset = QStringLiteral("The usage server connection changed. Waiting for a new reading.");

QString authSourceKey(const QVariantMap &provider) {
    const auto source = provider.value(QStringLiteral("auth")).toMap().value(QStringLiteral("source")).toMap();
    if (source.isEmpty()) return {};
    return source.value(QStringLiteral("kind")).toString() + QLatin1Char('\n') + source.value(QStringLiteral("name")).toString();
}

bool signedIn(const QVariantMap &provider) {
    if (provider.value(QStringLiteral("needs_reauth")).toBool()) return false;
    if (!provider.contains(QStringLiteral("auth"))) return true; // Older servers: no auth contract to contradict.
    return provider.value(QStringLiteral("auth")).toMap().value(QStringLiteral("state")).toString() == QStringLiteral("signed_in");
}
}

void Controller::markBackendUnavailable(bool outage, const QString &message) {
    // IPC, the tray, notifications, and reset eligibility read m_providers: a
    // successful entry must not outlive the connection that produced it.
    const QString error = outage ? backendUnreachable : message.isEmpty() ? m_message : message;
    for (auto &value : m_providers) {
        auto provider = value.toMap();
        const QString name = provider.value(QStringLiteral("provider_name")).toString();
        // Entries masked by an earlier failure follow the latest failure; genuine
        // provider errors from the last snapshot keep their own message.
        if (provider.value(QStringLiteral("is_success")).toBool()) m_maskedProviders.insert(name);
        else if (!m_maskedProviders.contains(name)) continue;
        provider[QStringLiteral("is_success")] = false;
        provider[QStringLiteral("error")] = error;
        provider[QStringLiteral("buckets")] = QVariantList{};
        provider[QStringLiteral("rate_limit_reset_credits")] = QVariant::fromValue(nullptr);
        value = provider;
    }
    m_backendOutage = outage;
    if (!outage) m_retained.clear();
}

void Controller::invalidateConnection() {
    ++m_connectionGeneration;
    cancel();
    // The peer that produced the current snapshot is gone (owned server exit,
    // replaced identity, or new settings): its live meters and credits must not
    // be published anywhere until the new peer answers. Only a new accepted
    // snapshot restores them.
    markBackendUnavailable(false, connectionReset);
    m_transientSince.clear();
    m_recovery.stop(); m_recoveryStep = 0; m_refreshBaseline.clear(); m_refreshAcceptedAt = 0;
    m_refreshAllowedAt = 0; m_refreshNoticeUntil = 0; m_refreshKind.clear(); m_refreshSummary.clear();
    syncRefreshClock();
    emit refreshStatusChanged();
}

bool Controller::transientRecoveryActive() const {
    const qint64 now = QDateTime::currentSecsSinceEpoch();
    for (const qint64 since : m_transientSince) if (now - since < transientRecoveryWindowSecs) return true;
    return false;
}

void Controller::retainReadings(const QVariantList &providers, bool removeMissing) {
    const qint64 now = QDateTime::currentSecsSinceEpoch();
    QSet<QString> seen;
    for (const auto &value : providers) {
        const auto provider = value.toMap();
        const QString name = provider["provider_name"].toString();
        seen.insert(name);
        const auto status = provider["fetch_status"].toMap();
        const QString source = authSourceKey(provider);
        const QString epoch = status["credential_epoch"].toString();
        if (provider["is_success"].toBool()) {
            auto &reading = m_retained[name];
            const QString fetchedAt = status["fetched_at"].toString();
            const auto buckets = provider["buckets"].toList();
            // A repeated read of the server's cache is the same reading. Another
            // credential epoch or source is a new reading even when its attempt
            // time reads the same, as is any change in the buckets themselves.
            const bool sameReading = !reading.buckets.isEmpty() && reading.epoch == epoch && reading.source == source
                && reading.fetchedAt == fetchedAt && reading.buckets == buckets;
            if (!sameReading) {
                reading.buckets = buckets; reading.fetchedAt = fetchedAt;
                // The age is the server's attempt completion time: a reading that was
                // already old when this session first read it stays that old. The
                // desktop clock only stands in for servers without the metadata, and
                // a server clock running ahead cannot make a reading newer than now.
                const auto at = QDateTime::fromString(fetchedAt, Qt::ISODateWithMs);
                reading.observedAt = at.isValid() ? qMin(at.toSecsSinceEpoch(), now) : now;
            }
            reading.epoch = epoch;
            reading.source = source;
            reading.snapshotSerial = m_snapshotSerial;
            continue;
        }
        const auto reading = m_retained.constFind(name);
        if (reading == m_retained.cend()) continue;
        const QString state = provider["auth"].toMap()["state"].toString();
        // A reading survives a failure only when that failure proves the same
        // signed-in context: the same nonempty credential epoch and source. A
        // sign-in change, another epoch, or lost metadata retires it for good, so
        // the old epoch returning later cannot resurrect another context's numbers.
        if (provider["needs_reauth"].toBool() || status["failure_kind"].toString() == QStringLiteral("auth")
            || state == QStringLiteral("expired") || state == QStringLiteral("signed_out")
            || epoch.isEmpty() || epoch != reading->epoch
            || (!source.isEmpty() && source != reading->source)) m_retained.erase(reading);
    }
    if (!removeMissing) return;
    for (auto it = m_retained.begin(); it != m_retained.end();) {
        if (seen.contains(it.key())) ++it; else it = m_retained.erase(it);
    }
}

QVariantList Controller::displayProviders() const {
    const qint64 now = QDateTime::currentSecsSinceEpoch();
    QVariantList result;
    for (const auto &value : providers()) {
        auto provider = value.toMap();
        const auto reading = m_retained.constFind(provider["provider_name"].toString());
        if (!provider["is_success"].toBool() && reading != m_retained.cend() && !reading->buckets.isEmpty()
            && now - reading->observedAt <= maximumStaleReadingSecs && signedIn(provider)) {
            const auto status = provider["fetch_status"].toMap();
            const QString kind = status["failure_kind"].toString(), epoch = status["credential_epoch"].toString();
            // Reuse only this connection's reading from the last good snapshot during
            // a backend outage, or the same credential epoch's reading while the
            // server itself reports a transient or rate-limited provider failure.
            const bool outage = m_backendOutage && reading->snapshotSerial == m_snapshotSerial;
            const bool transient = (kind == QStringLiteral("transient") || kind == QStringLiteral("rate_limited"))
                && !epoch.isEmpty() && epoch == reading->epoch;
            if (outage || transient) {
                provider["buckets"] = reading->buckets;
                provider["stale"] = true;
                provider["last_reading_at"] = reading->observedAt;
            }
        }
        result.append(provider);
    }
    return result;
}

QString Controller::readingAgeLabel(qint64 observedAt) const {
    if (observedAt <= 0) return {};
    const auto now = QDateTime::currentDateTime();
    const qint64 seconds = qMax<qint64>(0, now.toSecsSinceEpoch() - observedAt);
    const qint64 minutes = seconds / 60;
    QString relative;
    if (minutes < 1) relative = QStringLiteral("just now");
    else if (minutes < 60) relative = QStringLiteral("%1m ago").arg(minutes);
    else if (minutes < 1440) relative = QStringLiteral("%1h %2m ago").arg(minutes / 60).arg(minutes % 60);
    else relative = QStringLiteral("%1d %2h ago").arg(minutes / 1440).arg(minutes % 1440 / 60);
    const QString absolute = Usage::resetTimeLabel(QDateTime::fromSecsSinceEpoch(observedAt).toUTC().toString(Qt::ISODate), now);
    return absolute.isEmpty() ? relative : QStringLiteral("%1 (%2)").arg(relative, absolute);
}
