#include "controller.h"
#include "usage.h"
#include <QDateTime>
#include <QHostInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <cmath>
#include <limits>

namespace {
// Cache reads after an accepted refresh, in seconds from acceptance.
constexpr int recoveryOffsets[] = {2, 5, 10, 20, 35, 45};
constexpr int recoverySteps = int(sizeof(recoveryOffsets) / sizeof(recoveryOffsets[0]));

// The server's JSON delay is authoritative, including long all-provider rate
// limits: it is never shortened, only an unusable value falls back to 15 seconds.
int retryAfterSeconds(const QByteArray &body) {
    const auto value = QJsonDocument::fromJson(body).object().value(QStringLiteral("retry_after_seconds"));
    const double seconds = value.toDouble(-1);
    constexpr double maximum = double(std::numeric_limits<int>::max());
    if (!value.isDouble() || !std::isfinite(seconds) || seconds < 1 || seconds > maximum || std::floor(seconds) != seconds) return 15;
    return int(seconds);
}
}

void Controller::requestRefresh() {
    if (m_loading || m_resetBusy || QDateTime::currentMSecsSinceEpoch() < m_refreshAllowedAt) return;
    if (m_mode == "remote" && m_status == "offline") QHostInfo::clearCache();
    // Without a reachable server there is nothing to refresh: reconnect first.
    if (m_mode == "local" && !m_server.isAvailable()) { refreshUsage(true); return; }
    if (backendUrl().isEmpty()) { m_status = "setup"; m_errorKind.clear(); emit changed(); return; }
    if (m_mode == QStringLiteral("remote") && TlsProof::shouldUpgrade(QUrl(m_url), !m_token.isEmpty(),
        QDateTime::currentSecsSinceEpoch(), m_nextUpgradeAttempt)) { requestUsage(); return; }
    requestProviderRefresh();
}

void Controller::requestProviderRefresh() {
    const auto url = Usage::refreshEndpoint(backendUrl());
    if (url.isEmpty()) { fail("Enter a valid HTTP or HTTPS backend address in settings."); return; }
    const ServerConnection transport = m_mode == QStringLiteral("local")
        ? m_server.connection() : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), backendCertificate()};
    auto request = backendRequest(url, transport);
    request.setHeader(QNetworkRequest::ContentLengthHeader, 0);
    m_loading = true; log("Connection", "Requesting a provider refresh."); emit changed();
    auto reply = transportNetwork()->post(request, QByteArray()); m_reply = reply;
    guardReply(reply, transport, m_mode == QStringLiteral("ssh") ? 27000 : 12000, 4096);
    const quint64 generation = m_connectionGeneration;
    connect(reply, &QNetworkReply::finished, this, [this, reply, generation] {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
        const auto error = reply->error(); const auto body = reply->isOpen() ? reply->readAll() : QByteArray();
        const bool certificateFailure = reply->property("headroomPinMismatch").toBool() || reply->property("headroomCertificateErrors").toBool();
        reply->deleteLater(); m_reply.clear(); m_loading = false;
        if (generation != m_connectionGeneration) return;
        if (rejectRedirectOrToken(status, redirected)) return;
        if (status == 202) { acceptRefresh(body); return; }
        if (status == 429) { limitRefresh(body); return; }
        const QString errorText = body.size() <= 4096 ? QJsonDocument::fromJson(body).object().value(QStringLiteral("error")).toString() : QString();
        // Servers predating the endpoint answer 404/405 over HTTP; the fixed SSH
        // receiver rejects an unknown frame with 400 "invalid request".
        if (status == 404 || status == 405 || (status == 400 && m_mode == QStringLiteral("ssh") && errorText == QStringLiteral("invalid request"))) {
            noteRefresh(QStringLiteral("unsupported"), QStringLiteral("This server does not support refreshing providers on request.")); return;
        }
        if (status == 503) { noteRefresh(QStringLiteral("unavailable"), QStringLiteral("The server could not refresh providers right now.")); return; }
        if (status) { noteRefresh(QStringLiteral("unexpected"), QString("The server did not accept the refresh request (HTTP %1).").arg(status)); return; }
        if (error != QNetworkReply::NoError) { reportNetworkFailure(certificateFailure); return; }
        noteRefresh(QStringLiteral("unexpected"), QStringLiteral("The server returned an unexpected refresh response."));
    });
}

void Controller::acceptRefresh(const QByteArray &body) {
    const QString status = QJsonDocument::fromJson(body).object().value(QStringLiteral("status")).toString();
    const qint64 now = QDateTime::currentMSecsSinceEpoch();
    m_refreshAcceptedAt = now;
    m_refreshAllowedAt = now + qint64(retryAfterSeconds(body)) * 1000;
    m_refreshKind = status == QStringLiteral("coalesced") ? QStringLiteral("coalesced") : QStringLiteral("accepted");
    m_refreshSummary.clear();
    // Remember each provider's current attempt time: once every entry reports a
    // later one, the server has finished this round and the window can end early.
    m_refreshBaseline.clear();
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        const QString fetchedAt = provider.value(QStringLiteral("fetch_status")).toMap().value(QStringLiteral("fetched_at")).toString();
        if (!fetchedAt.isEmpty()) m_refreshBaseline.insert(provider.value(QStringLiteral("provider_name")).toString(), fetchedAt);
    }
    m_recoveryStep = 0;
    m_recovery.start(recoveryOffsets[0] * 1000);
    log("Connection", m_refreshKind == QStringLiteral("coalesced")
        ? "A provider refresh is already in progress; reading the cache while it completes."
        : "Provider refresh accepted; reading the cache while providers refetch.");
    if (m_startPolling && !m_poll.isActive()) m_poll.start(m_interval * 1000);
    syncRefreshClock(); emit refreshStatusChanged(); emit changed();
}

void Controller::limitRefresh(const QByteArray &body) {
    const int seconds = retryAfterSeconds(body);
    m_refreshAllowedAt = QDateTime::currentMSecsSinceEpoch() + qint64(seconds) * 1000;
    m_refreshKind = QStringLiteral("limited"); m_refreshSummary.clear();
    log("Connection", QString("Provider refresh is rate limited; reading the cache instead. Try again in %1 seconds.").arg(seconds));
    syncRefreshClock(); emit refreshStatusChanged();
    refreshUsage(false);
}

void Controller::noteRefresh(const QString &kind, const QString &summary) {
    m_refreshKind = kind; m_refreshSummary = summary;
    m_refreshNoticeUntil = QDateTime::currentMSecsSinceEpoch() + 20000;
    log("Connection", summary + " Reading the cached usage instead.");
    syncRefreshClock(); emit refreshStatusChanged();
    refreshUsage(false);
}

void Controller::runRecoveryStep() {
    // One request at a time; a skipped step is never queued for catch-up.
    const qint64 window = m_refreshAcceptedAt;
    if (!m_loading && !m_resetBusy) refreshUsage(false);
    // A synchronous failure or connection change may already have ended this window.
    if (window == 0 || m_refreshAcceptedAt != window) return;
    if (++m_recoveryStep >= recoverySteps) { endRefreshWindow(); return; }
    const qint64 due = m_refreshAcceptedAt + qint64(recoveryOffsets[m_recoveryStep]) * 1000;
    m_recovery.start(int(qMax<qint64>(0, due - QDateTime::currentMSecsSinceEpoch())));
}

void Controller::endRefreshWindow() {
    const bool active = m_recovery.isActive() || m_refreshAcceptedAt != 0;
    m_recovery.stop(); m_recoveryStep = 0; m_refreshBaseline.clear(); m_refreshAcceptedAt = 0;
    if (!active) return;
    syncRefreshClock(); emit refreshStatusChanged();
}

bool Controller::refreshObserved(const QVariantList &providers) const {
    if (m_refreshBaseline.isEmpty()) return false;
    for (const auto &value : providers) {
        const auto provider = value.toMap();
        const QString fetchedAt = provider.value(QStringLiteral("fetch_status")).toMap().value(QStringLiteral("fetched_at")).toString();
        const auto baseline = m_refreshBaseline.constFind(provider.value(QStringLiteral("provider_name")).toString());
        if (fetchedAt.isEmpty() || baseline == m_refreshBaseline.cend() || fetchedAt == *baseline) return false;
        const auto before = QDateTime::fromString(*baseline, Qt::ISODateWithMs), after = QDateTime::fromString(fetchedAt, Qt::ISODateWithMs);
        if (!before.isValid() || !after.isValid() || after < before) return false;
    }
    return true;
}

void Controller::syncRefreshClock() {
    const qint64 now = QDateTime::currentMSecsSinceEpoch();
    const bool needed = m_recovery.isActive() || now < m_refreshAllowedAt || (!m_refreshSummary.isEmpty() && now < m_refreshNoticeUntil);
    if (needed) { if (!m_refreshClock.isActive()) m_refreshClock.start(); return; }
    m_refreshClock.stop();
    m_refreshKind.clear(); m_refreshSummary.clear();
}

QVariantMap Controller::refreshStatus() const {
    const qint64 now = QDateTime::currentMSecsSinceEpoch();
    const int retry = m_refreshAllowedAt > now ? int((m_refreshAllowedAt - now + 999) / 1000) : 0;
    const bool active = m_recovery.isActive();
    QString notice;
    if (m_refreshKind == QStringLiteral("accepted") || m_refreshKind == QStringLiteral("coalesced")) {
        if (active) notice = m_refreshKind == QStringLiteral("coalesced") ? QStringLiteral("Refresh already underway · reading new usage…")
                                                                            : QStringLiteral("Refresh requested · reading new usage…");
    } else if (m_refreshKind == QStringLiteral("limited")) {
        if (retry > 0) notice = QString("Refresh limited · try again in %1s").arg(retry);
    } else if (!m_refreshSummary.isEmpty() && now < m_refreshNoticeUntil) notice = m_refreshSummary;
    return {{"retrySeconds", retry}, {"active", active}, {"kind", m_refreshKind}, {"notice", notice}};
}
