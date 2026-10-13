#include "controller.h"
#include "usage.h"
#include "logincopy.h"
#include <QClipboard>
#include <QGuiApplication>
#include <QDateTime>
#include <QNetworkProxy>
#include <QHostInfo>
#include <QUrl>
#include <utility>

namespace {
// Network-only retry schedule; token, API, and certificate failures keep the slower backoff.
constexpr int networkRetrySecs[] = {5, 10, 20, 40, 60, 120, 240, 300};
}

Controller::Controller(const QString &configPath, QObject *parent, bool allowAutomaticMigration,
                       ManagedServerOptions serverOptions, CredentialServiceOptions credentialOptions, SshOptions sshOptions, bool startPolling)
    : QObject(parent), m_settingsService(configPath, allowAutomaticMigration),
      m_startPolling(startPolling), m_sshNetwork(sshOptions),
      m_server(std::move(serverOptions), this),
      m_credentials([&] {
          if (credentialOptions.sshOptions.executablePath.isEmpty()) credentialOptions.sshOptions = sshOptions;
          return std::move(credentialOptions);
      }(), this) {
    const auto &loaded = m_settingsService.value();
    m_mode = loaded.connectionMode; m_url = loaded.url; m_token = loaded.token; m_sshUrl = loaded.sshUrl;
    m_interval = loaded.interval; m_notifications = loaded.notifications;
    m_primary = loaded.primary; m_order = loaded.order;
    if (!m_settingsService.loadError().isEmpty()) m_message = m_settingsService.loadError();
    log("App", "Usage monitor started.");
    if (!m_settingsService.migrationNotice().isEmpty()) log("Settings", m_settingsService.migrationNotice());
    m_poll.setSingleShot(true);
    m_poll.setTimerType(Qt::PreciseTimer);
    connect(&m_poll, &QTimer::timeout, this, [this] { refreshUsage(false); });
    if (m_startPolling) m_poll.start(m_interval * 1000);
    connect(&m_clock, &QTimer::timeout, this, [this] { expireScheduledChatGptReset(); updateMeterStates(); emit changed(); });
    if (m_startPolling) m_clock.start(30000);
    m_recovery.setSingleShot(true);
    m_recovery.setTimerType(Qt::PreciseTimer);
    connect(&m_recovery, &QTimer::timeout, this, &Controller::runRecoveryStep);
    m_refreshClock.setInterval(1000);
    connect(&m_refreshClock, &QTimer::timeout, this, [this] { syncRefreshClock(); emit refreshStatusChanged(); });
    m_localNetwork.setProxy(QNetworkProxy::NoProxy);
    m_server.configure(m_mode, m_token);
    syncConnection();
    // Start a pending HTTPS upgrade at once; version checks wait for its result.
    if (m_startPolling && remoteVerificationPending()) QTimer::singleShot(0, this, [this] { requestUsage(); });
    connect(&m_credentials, &CredentialService::event, this, [this](const QString &message) {
        log(QStringLiteral("Credentials"), message);
    });
    connect(&m_credentials, &CredentialService::checkedChanged, this, [this] { emit settingsChanged(); emit providersChanged(); });
    connect(&m_credentials, &CredentialService::providerRecovered, this, [this](const QVariantMap &replacement) {
        const QString name = replacement.value(QStringLiteral("provider_name")).toString();
        bool replaced = false;
        for (auto &provider : m_providers) {
            if (provider.toMap().value(QStringLiteral("provider_name")).toString() != name) continue;
            provider = replacement;
            replaced = true;
            break;
        }
        if (!replaced) return;
        m_maskedProviders.remove(name);
        retainReadings({replacement}, false);
        m_notificationCenter.resetUsageBaseline(name);
        m_notificationCenter.observeUsage(m_providers, false);
        updateMeterStates();
        emit providersChanged();
        emit changed();
        m_credentials.consider(m_providers);
    });
    connect(&m_server, &ManagedServer::available, this, [this] {
        if (m_mode == "local" && !m_loading && !m_waitingForUsageRetry) requestUsage();
    });
    connect(&m_server, &ManagedServer::unavailable, this, [this](const QString &message, const QString &kind) {
        if (m_mode != "local") return;
        cancelResetRequest();
        cancel(); m_poll.stop(); m_waitingForUsageRetry = false;
        endRefreshWindow();
        m_status = "offline"; m_message = message; m_errorKind = kind;
        // A lost local server masks the raw model like any backend failure. Only
        // an unreachable or unresponsive server is an outage that may keep this
        // session's last reading on the dashboard; an incompatible, rejecting, or
        // replaced peer means those readings are no longer this server's.
        markBackendUnavailable(kind == QStringLiteral("network") || kind == QStringLiteral("timeout"));
        log("Local server", message); emit providersChanged(); emit changed();
    });
    connect(&m_server, &ManagedServer::connectionChanged, this, [this] {
        if (m_mode != QStringLiteral("local")) return;
        cancelResetRequest();
        // A new server identity is a new peer: late replies and retained
        // readings from the previous process must not survive it.
        invalidateConnection();
        m_localNetwork.clearConnectionCache();
        syncConnection();
        emit providersChanged(); emit changed();
    });
    connect(&m_server, &ManagedServer::stateChanged, this, [this] {
        if (m_mode != "local" || m_server.isAvailable() || m_server.state() == "failed") return;
        if (m_loading) cancel();
        if (!m_waitingForUsageRetry) m_poll.stop();
        // Keep an established failure visible while Local mode probes or starts
        // its server. Recovery is complete only after a new snapshot is accepted.
        if (m_status != QStringLiteral("offline")) {
            m_status = QStringLiteral("connecting");
            m_message = QStringLiteral("Preparing the local usage server…");
            m_errorKind.clear();
        }
        emit changed();
    });
    if (m_startPolling) QTimer::singleShot(0, this, &Controller::refresh);
}

QVariantMap Controller::settings() const {
    QString connectionStatus;
    if (QUrl(m_url).scheme() == QStringLiteral("https")) connectionStatus = m_settingsService.value().remoteCertificate.isEmpty()
        ? QStringLiteral("Encrypted.") : QStringLiteral("Encrypted. The server's identity was verified with your access token.");
    else connectionStatus = m_token.isEmpty() ? QStringLiteral("Not encrypted.")
        : QStringLiteral("Not encrypted. Headroom switches to HTTPS automatically when the server supports it.");
    return {{"mode", m_mode}, {"url", m_url}, {"sshUrl", m_sshUrl}, {"hasToken", !m_token.isEmpty()}, {"interval", m_interval},
        {"notifications", m_notifications}, {"primary", primary()}, {"connectionStatus", connectionStatus},
        {"shareBrowserSignIns", m_settingsService.value().shareBrowserSignIns}, {"browserSharingStatus", m_credentials.sharingStatus()}};
}
QVariantMap Controller::state() const {
    const auto elapsed = m_lastGood ? QDateTime::currentSecsSinceEpoch() - m_lastGood : 0;
    return {{"status", m_status}, {"message", m_message}, {"loading", m_loading},
        {"errorKind", m_errorKind}, {"retryAttempt", m_retryAttempt},
        {"retrySeconds", m_poll.isActive() ? (m_poll.remainingTime() + 999) / 1000 : 0}, {"lastGood", m_lastGood}, {"age", elapsed}, {"host", QUrl(backendUrl()).host()},
        {"updated", m_lastGood ? (elapsed < 60 ? "Just updated" : QString("Updated %1m ago").arg(elapsed / 60)) : "Awaiting connection"}};
}
QString Controller::backendUrl() const {
    return m_mode == "local" ? m_server.connection().url.toString() : (m_mode == "ssh" ? m_sshUrl : m_url);
}
QString Controller::backendToken() const {
    return m_mode == "local" ? QString::fromUtf8(m_server.connection().token) : (m_mode == "ssh" ? QString() : m_token);
}
QSslCertificate Controller::backendCertificate() const {
    return m_mode == "local" ? m_server.connection().certificate
        : m_mode == "remote" ? QSslCertificate(m_settingsService.value().remoteCertificate.toUtf8()) : QSslCertificate();
}
void Controller::syncConnection() {
    if (m_mode == QStringLiteral("local")) {
        const auto transport = m_server.connection();
        m_credentials.configure(m_mode, transport.url.toString(), QString::fromUtf8(transport.token), transport.certificate, m_settingsService.value().shareBrowserSignIns);
    } else {
        m_credentials.configure(m_mode, backendUrl(), backendToken(), backendCertificate(), m_settingsService.value().shareBrowserSignIns);
    }
}
QString Controller::displayName(const QString &provider) const { return Usage::displayName(provider); }
QVariantMap Controller::loginCopy(const QVariantMap &provider) const {
    return LoginCopy::compose(provider, m_mode, m_credentials.sharingStatus(),
                              m_credentials.checked(provider.value(QStringLiteral("provider_name")).toString()));
}
QVariantMap Controller::browserChecked() const {
    return {{QStringLiteral("Cursor"), m_credentials.checked(QStringLiteral("Cursor"))},
            {QStringLiteral("Grok"), m_credentials.checked(QStringLiteral("Grok"))}};
}

bool Controller::remoteVerificationPending() const {
    return m_mode == QStringLiteral("remote") && (m_proving || TlsProof::shouldUpgrade(QUrl(m_url), !m_token.isEmpty(),
        QDateTime::currentSecsSinceEpoch(), m_nextUpgradeAttempt));
}

void Controller::verifyRemote(bool upgrade) {
    if (m_proving || m_mode != QStringLiteral("remote") || m_token.isEmpty()) return;
    m_proving = true; m_loading = true; m_proofAttempted = true;
    const bool rotation = !backendCertificate().isNull();
    m_nextUpgradeAttempt = QDateTime::currentSecsSinceEpoch() + 6 * 3600;
    emit changed();
    m_tlsProof.request(QUrl(m_url), m_token.toUtf8(), [this, upgrade, rotation](TlsProof::Result result) {
        m_proving = false; m_loading = false;
        if (!result.verified()) {
            log(QStringLiteral("Connection"), QStringLiteral("Token-verified HTTPS proof did not succeed."));
            if (upgrade) { requestUsage(); return; }
            fail(QStringLiteral("The server certificate could not be verified with your access token. Check the server and connection settings."), QStringLiteral("certificate"));
            return;
        }
        auto updated = m_settingsService.value();
        if (upgrade) updated.url = TlsProof::upgradeUrl(QUrl(m_url)).toString();
        updated.remoteCertificate = QString::fromUtf8(result.certificate.toPem());
        const QString error = m_settingsService.save(updated);
        if (!error.isEmpty()) { fail(QStringLiteral("The verified server certificate could not be saved."), QStringLiteral("settings")); return; }
        m_url = m_settingsService.value().url;
        cancelResetRequest(); cancelScheduledChatGptReset(); invalidateConnection(); m_network.clearConnectionCache(); syncConnection();
        log(QStringLiteral("Connection"), upgrade ? QStringLiteral("Connection upgraded to HTTPS.")
            : rotation ? QStringLiteral("The server certificate changed and was verified with your access token.")
                       : QStringLiteral("The server's identity was verified with your access token."));
        emit settingsChanged(); emit changed(); requestUsage();
    });
}

void Controller::cancel() {
    if (m_reply) { disconnect(m_reply, nullptr, this, nullptr); m_reply->abort(); m_reply->deleteLater(); m_reply.clear(); }
    m_loading = false;
}
void Controller::log(const QString &category, const QString &message) {
    // Only developer-authored event summaries belong here. Never pass URLs,
    // headers, response bodies, provider errors, account labels, or credentials.
    m_diagnostics.append(QVariantMap{{"time", QDateTime::currentDateTimeUtc().toString(Qt::ISODate)},
        {"category", category}, {"message", message}});
    while (m_diagnostics.size() > 500) m_diagnostics.removeFirst();
    emit diagnosticsChanged();
}
void Controller::clearDiagnostics() { m_diagnostics.clear(); emit diagnosticsChanged(); }
QString Controller::diagnosticText() const {
    QStringList lines;
    for (const auto &item : m_diagnostics) {
        const auto row = item.toMap();
        lines.append(row["time"].toString() + " [" + row["category"].toString() + "] " + row["message"].toString());
    }
    return lines.join('\n');
}
void Controller::resetRetry() {
    m_retryAttempt = 0; m_errorKind.clear();
    int seconds = m_interval;
    if (!m_autoResetConnection.isEmpty()) seconds = 15;
    // A provider the server reports as transiently failed usually recovers on
    // the server's own retry schedule; read its cache a little sooner for a
    // bounded time instead of waiting a full interval to notice.
    else if (transientRecoveryActive()) seconds = qMin(m_interval, 15);
    if (m_startPolling) m_poll.start(seconds * 1000);
}
void Controller::fail(const QString &message, const QString &kind) {
    m_status = "offline"; m_message = message; m_loading = false; m_errorKind = kind;
    m_retryAttempt = qMin(m_retryAttempt + 1, 8);
    endRefreshWindow();
    // An armed one-shot reset starts from the 15-second cadence used by
    // resetRetry(), so a transient failure cannot stretch polling past its
    // weekly window. It still backs off to at most five minutes: a long outage
    // must not poll an unreachable endpoint four times a minute all week.
    int seconds;
    if (!m_autoResetConnection.isEmpty()) seconds = qMin(15 * (1 << (m_retryAttempt - 1)), 300);
    else if (kind == QStringLiteral("network")) seconds = networkRetrySecs[qMin(m_retryAttempt - 1, 7)];
    else seconds = qMin(m_interval * (1 << m_retryAttempt), qMax(300, m_interval));
    if (m_startPolling) m_poll.start(seconds * 1000);
    // No backend failure passes old success through the raw model. Only a
    // plain network failure is an outage whose last reading the dashboard may
    // keep; a rejected token or certificate, a redirect, an HTTP error, or a
    // malformed body means the peer may not be the server that produced it.
    markBackendUnavailable(kind == QStringLiteral("network"));
    log("Connection", message + QString(" Retry in %1 seconds.").arg(seconds));
    emit providersChanged(); emit changed();
}
void Controller::refresh() {
    refreshUsage(true);
}
QNetworkRequest Controller::backendRequest(const QUrl &url, const ServerConnection &transport) const {
    QNetworkRequest request(url);
    request.setRawHeader("Accept", "application/json");
    request.setRawHeader("User-Agent", "Headroom/" HEADROOM_VERSION);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(10000);
    if (!transport.token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + transport.token);
    ServerTransport::secureRequest(request, transport.certificate, m_mode == QStringLiteral("remote"));
    return request;
}
QNetworkAccessManager *Controller::transportNetwork() {
    return m_mode == QStringLiteral("local") ? &m_localNetwork
        : m_mode == QStringLiteral("ssh") ? static_cast<QNetworkAccessManager *>(&m_sshNetwork) : &m_network;
}
void Controller::guardReply(QNetworkReply *reply, const ServerConnection &transport, int deadlineMs, qint64 maximumBytes) {
    connect(reply, &QNetworkReply::sslErrors, reply, [reply](const QList<QSslError> &errors) {
        if (!errors.isEmpty()) reply->setProperty("headroomCertificateErrors", true);
    });
    ServerTransport::requirePinnedPeer(reply, transport.certificate, m_mode == QStringLiteral("remote"));
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(deadlineMs);
    connect(reply, &QNetworkReply::readyRead, this, [reply, maximumBytes] { if (reply->bytesAvailable() > maximumBytes) reply->abort(); });
}
bool Controller::rejectRedirectOrToken(int status, bool redirected) {
    if (redirected) { fail("The backend redirected the request. Enter its final address in settings.", "api"); return true; }
    if (status == 401 || status == 403) { fail("Your backend rejected the token. Update it in connection settings.", "auth"); return true; }
    return false;
}
void Controller::reportNetworkFailure(bool certificateFailure) {
    if (m_mode == QStringLiteral("remote") && QUrl(m_url).scheme() == QStringLiteral("https") && certificateFailure) {
        if (!m_token.isEmpty() && (!m_proofAttempted || QDateTime::currentSecsSinceEpoch() >= m_nextUpgradeAttempt)) { verifyRemote(false); return; }
        fail(QStringLiteral("The server certificate could not be verified. Check the server and access token; HTTPS will not be downgraded."), QStringLiteral("certificate")); return;
    }
    if (m_mode == "local") {
        m_waitingForUsageRetry = true;
        m_server.reportConnectionFailure();
        fail("Cannot reach the local usage server. Headroom will recheck it before retrying.");
    } else if (m_mode == "ssh") fail("Cannot connect over SSH. Make sure OpenSSH is installed, then check the address, trusted host key, and key or agent authentication.");
    else fail("Cannot reach your backend. Check your network and connection settings.");
}
void Controller::refreshUsage(bool userRequested) {
    if (m_loading || m_resetBusy) return;
    if (userRequested && m_mode == "remote" && m_status == "offline") QHostInfo::clearCache();
    m_poll.stop();
    if (m_mode == "local") {
        m_waitingForUsageRetry = false;
        if (m_status != QStringLiteral("offline")) {
            m_status = QStringLiteral("connecting");
            m_message = QStringLiteral("Preparing the local usage server…");
            m_errorKind.clear();
        }
        emit changed();
        m_server.ensureAvailable();
        return;
    }
    if (backendUrl().isEmpty()) { m_status = "setup"; m_errorKind.clear(); emit changed(); return; }
    requestUsage();
}
void Controller::requestUsage() {
    if (m_loading || m_resetBusy) return;
    m_poll.stop();
    if (m_mode == QStringLiteral("remote") && TlsProof::shouldUpgrade(QUrl(m_url), !m_token.isEmpty(),
        QDateTime::currentSecsSinceEpoch(), m_nextUpgradeAttempt)) { verifyRemote(true); return; }
    const auto url = Usage::endpoint(backendUrl());
    if (url.isEmpty()) { fail("Enter a valid HTTP or HTTPS backend address in settings."); return; }
    const ServerConnection transport = m_mode == QStringLiteral("local")
        ? m_server.connection() : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), backendCertificate()};
    const auto request = backendRequest(url, transport);
    m_loading = true; log("Connection", "Requesting usage snapshot."); emit changed();
    auto reply = transportNetwork()->get(request); m_reply = reply;
    guardReply(reply, transport, m_mode == QStringLiteral("ssh") ? 27000 : 12000, 1024 * 1024);
    const quint64 generation = m_connectionGeneration;
    connect(reply, &QNetworkReply::finished, this, [this, reply, generation] {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
        const auto error = reply->error(); const auto body = reply->isOpen() ? reply->readAll() : QByteArray();
        const bool certificateFailure = reply->property("headroomPinMismatch").toBool() || reply->property("headroomCertificateErrors").toBool();
        reply->deleteLater(); m_reply.clear(); m_loading = false;
        if (generation != m_connectionGeneration) return;
        if (rejectRedirectOrToken(status, redirected)) return;
        if (status && status != 200) { fail(QString("Backend returned HTTP %1. Check the address and try again.").arg(status), "api"); return; }
        if (error != QNetworkReply::NoError) { reportNetworkFailure(certificateFailure); return; }
        QVariantList providers;
        if (body.size() > 1024 * 1024 || !Usage::parse(body, providers)) { fail("The backend returned an unexpected usage response.", "malformed"); return; }
        acceptSnapshot(providers);
        m_proofAttempted = false;
    });
}
void Controller::acceptSnapshot(const QVariantList &providers) {
    ++m_snapshotSerial;
    m_backendOutage = false;
    retainReadings(providers, true);
    const qint64 now = QDateTime::currentSecsSinceEpoch();
    QSet<QString> transient;
    int failed = 0;
    for (const auto &value : providers) {
        const auto provider = value.toMap();
        if (provider["is_success"].toBool()) continue;
        ++failed;
        if (provider["fetch_status"].toMap()["failure_kind"].toString() != QStringLiteral("transient")) continue;
        const QString name = provider["provider_name"].toString();
        transient.insert(name);
        if (!m_transientSince.contains(name)) m_transientSince.insert(name, now);
    }
    for (auto it = m_transientSince.begin(); it != m_transientSince.end();) {
        if (transient.contains(it.key())) ++it; else it = m_transientSince.erase(it);
    }
    resetRetry();
    m_waitingForUsageRetry = false;
    log("Connection", QString("Snapshot received: %1 providers, %2 unavailable.").arg(providers.size()).arg(failed));
    m_providers = providers; m_maskedProviders.clear(); m_lastGood = now; m_status = "ready"; m_message.clear();
    // An accepted refresh never counts as fresh data by itself; only a snapshot
    // whose every attempt time advanced proves the server finished the round.
    if (m_recovery.isActive() && refreshObserved(providers)) endRefreshWindow();
    observeResetUsage();
    m_notificationCenter.observeUsage(m_providers, m_notifications);
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        if (provider["provider_name"].toString() != "Codex" || !provider["is_success"].toBool()) continue;
        const auto credits = provider["rate_limit_reset_credits"].toMap();
        if (credits.contains("available_count"))
            m_notificationCenter.observeBankedResetCount(credits["available_count"].toLongLong(),
                credits["account_fingerprint"].toString(), m_notifications);
    }
    updateMeterStates(); emit providersChanged(); emit settingsChanged(); emit changed();
    m_credentials.consider(m_providers);
    observeScheduledChatGptReset();
}
Controller::~Controller() {
    // The network manager outlives every other member, so an in-flight reply must be
    // disconnected and aborted before the members its handler touches are destroyed.
    cancel();
    cancelResetRequest();
    m_tlsProof.cancel();
}
QString Controller::saveSettings(QString mode, QString url, QString token, int interval, bool notifications, QString primary, bool forgetToken, QString sshUrl, bool shareBrowserSignIns) {
    if (m_resetBusy) return "Wait for the reset request to finish before changing connections.";
    m_resetConfirmation.clear();
    if (mode != "local" && mode != "remote" && mode != "ssh") mode = "remote";
    url = url.trimmed(); token = token.trimmed(); sshUrl = sshUrl.trimmed();
    if (mode == QStringLiteral("ssh")) { url = m_url; token = m_token; }
    if (mode == "local") url = Usage::endpoint(m_url).isEmpty() ? QString() : m_url;
    if (mode == "remote" && Usage::endpoint(url).isEmpty()) return "Use an HTTP or HTTPS address without credentials, a query, or a fragment.";
    if (mode == "ssh" && !SshTransport::parseAddress(sshUrl)) return "Use ssh://[user@]host[:port] without a password, path, query, or fragment.";
    if (token.contains('\n') || token.contains('\r')) return "The bearer token must be a single line.";
    if (interval < 15 || interval > 900) return "Choose a refresh interval between 15 and 900 seconds.";
    const QString savedToken = forgetToken && mode == "remote" ? QString()
        : (token.isEmpty() && (mode != "remote" || url == m_url) ? m_token : token);
    const QString retainedSshUrl = sshUrl.isEmpty() && mode != "ssh" ? m_sshUrl : sshUrl;
    const bool connectionChanged = m_mode != mode || m_url != url || m_token != savedToken || m_sshUrl != retainedSshUrl;
    const QString error = writeSettings(mode, url, savedToken, retainedSshUrl, interval, notifications, primary, shareBrowserSignIns);
    if (!error.isEmpty()) return error;
    if (mode == "remote") QHostInfo::clearCache();
    cancel();
    m_tlsProof.cancel(); m_proving = false;
    if (connectionChanged) { m_proofAttempted = false; m_nextUpgradeAttempt = 0; invalidateConnection(); }
    m_network.clearConnectionCache();
    m_waitingForUsageRetry = false;
    if (connectionChanged) { m_providers.clear(); m_maskedProviders.clear(); m_lastGood = 0; m_warningStates.clear(); m_concerns.clear(); m_notificationCenter.resetBankedResetBaseline(); m_notificationCenter.resetUsageBaseline(); cancelScheduledChatGptReset(); }
    m_mode = mode; m_url = url; m_token = savedToken; m_sshUrl = retainedSshUrl; m_interval = interval; m_notifications = notifications; m_primary = primary;
    m_server.configure(m_mode, m_token);
    syncConnection();
    m_status = "connecting"; m_message.clear(); resetRetry();
    log("Settings", "Connection settings saved.");
    emit settingsChanged(); emit providersChanged(); emit changed(); refresh(); return {};
}
QString Controller::writeSettings(const QString &mode, const QString &url, const QString &token, const QString &sshUrl, int interval, bool notifications, const QString &primary, bool shareBrowserSignIns) {
    DesktopSettings updated = m_settingsService.value();
    updated.connectionMode = mode; updated.url = url; updated.token = token;
    updated.sshUrl = sshUrl;
    updated.shareBrowserSignIns = shareBrowserSignIns;
    updated.interval = interval; updated.notifications = notifications;
    updated.primary = primary; updated.order = m_order;
    return m_settingsService.save(updated, true);
}
QVariantList Controller::providers() const {
    auto result = m_providers;
    std::stable_sort(result.begin(), result.end(), [&](const QVariant &a, const QVariant &b) {
        auto rank = [&](const QVariant &v) { const auto i = m_order.indexOf(v.toMap()["provider_name"].toString()); return i < 0 ? 999 : i; };
        return rank(a) < rank(b);
    });
    return result;
}
QString Controller::primary() const {
    const auto ordered = providers();
    return ordered.isEmpty() ? (m_order.isEmpty() ? m_primary : m_order.first()) : ordered.first().toMap()["provider_name"].toString();
}
void Controller::moveProvider(const QString &source, const QString &target, bool after) {
    if (source == target) return;
    QStringList order;
    for (const auto &p : providers()) order.append(p.toMap()["provider_name"].toString());
    if (!order.contains(source) || !order.contains(target)) return;
    order.removeAll(source); order.insert(order.indexOf(target) + (after ? 1 : 0), source);
    for (const auto &name : m_order) if (!order.contains(name)) order.append(name);
    const auto previous = m_order; m_order = order;
    const QString error = m_settingsService.saveOrder(order, order.first());
    if (!error.isEmpty()) {
        m_order = previous;
        m_notificationCenter.post("providerCard_" + source, "Order could not be saved", error, 2);
        return;
    }
    log("Settings", "Provider order changed.");
    m_primary = order.first(); emit settingsChanged(); emit providersChanged(); emit changed();
}

QVariantMap Controller::concern(const QString &provider, const QVariantMap &bucket) const {
    return m_concerns.value(qMakePair(provider, bucket["id"].toString()));
}
QString Controller::warningColor(int severity) const { return Usage::warningColor(Usage::WarningLevel(qBound(0, severity, 3))); }
void Controller::updateMeterStates() {
    // Don't infer recovery from old usage readings during a connection outage.
    if (m_status != "ready") return;
    const auto now = QDateTime::currentDateTimeUtc();
    QSet<MeterKey> present;
    QSet<QString> failedProviders;
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        const auto name = provider["provider_name"].toString();
        if (!provider["is_success"].toBool()) { failedProviders.insert(name); continue; }
        for (const auto &item : provider["buckets"].toList()) {
            const auto bucket = item.toMap();
            const MeterKey key(name, bucket["id"].toString());
            present.insert(key);
            auto assessment = Usage::concern(name, bucket, now);
            const auto reset = QDateTime::fromString(bucket["resets_at"].toString(), Qt::ISODateWithMs);
            const auto window = reset.isValid() ? QString::number(reset.toMSecsSinceEpoch()) : QString();
            // Once a known window expires, wait for its replacement. Reusing
            // cached usage with percentage-only fallback would create false alerts.
            const auto prior = m_warningStates.constFind(key);
            if (reset.isValid() && reset <= now && prior != m_warningStates.cend()
                && prior->initialized && prior->window == window) {
                auto retained = m_concerns.value(key);
                retained["available"] = false;
                retained["detail"] = "The reset time has passed. Waiting for the backend's replacement window.\nState: "
                    + Usage::warningName(prior->level) + ". The previous warning state is retained until fresh window data arrives.";
                m_concerns.insert(key, retained);
                continue;
            }
            auto transition = Usage::advanceWarning(m_warningStates[key], bucket["utilization"].toDouble(),
                assessment["available"].toBool(), assessment["pressure"].toDouble(), window);
            assessment["severity"] = int(transition.to);
            assessment["level"] = Usage::warningName(transition.to);
            assessment["color"] = Usage::warningColor(transition.to);
            assessment["detail"] = assessment["detail"].toString() + "\nState: " + Usage::warningName(transition.to)
                + ". Lower recovery thresholds prevent repeated alerts near a boundary.";
            m_concerns.insert(key, assessment);
            if (transition.changed)
                log("Warning", "Meter transitioned from " + Usage::warningName(transition.from) + " to " + Usage::warningName(transition.to) + ".");
            if (transition.notify && m_notifications) {
                const QString title = Usage::displayName(name) + " · " + bucket["label"].toString()
                    + " · " + Usage::warningName(transition.to);
                const QString message = QString("%1: %2% used, %3% remaining. %4")
                    .arg(bucket["label"].toString()).arg(bucket["utilization"].toDouble(), 0, 'f', 1)
                    .arg(assessment["remaining"].toDouble(), 0, 'f', 1)
                    .arg(assessment["available"].toBool() ? QString("%1% of the remaining allowance was spent ahead of pace.").arg(assessment["pressure"].toDouble() * 100, 0, 'f', 0) : QString("Pacing unavailable."));
                m_notificationCenter.post("meter_" + name + "_" + bucket["id"].toString(), title, message, int(transition.to));
            }
        }
    }
    for (const auto &key : m_warningStates.keys()) {
        if (!present.contains(key) && !failedProviders.contains(key.first)) {
            m_warningStates.remove(key); m_concerns.remove(key);
        }
    }
}
QVariantList Controller::notches(const QString &provider, const QVariantMap &bucket) const { return Usage::notches(provider, bucket); }
QVariantMap Controller::pacing(const QString &provider, const QVariantMap &bucket) const { return Usage::pacing(provider, bucket); }
QString Controller::countdown(const QString &timestamp) const { return Usage::countdown(timestamp); }
QString Controller::resetTimeLabel(const QString &timestamp) const { return Usage::resetTimeLabel(timestamp); }
void Controller::setPrimary(const QString &name) {
    moveProvider(name, primary(), false);
}
QString Controller::saveStartupPreference(bool enabled) {
    const QString error = m_settingsService.saveStartupPreference(enabled);
    if (error.isEmpty()) emit settingsChanged();
    return error;
}
QString Controller::completeStartupMigration() {
    const QString error = m_settingsService.completeStartupMigration();
    if (error.isEmpty()) emit settingsChanged();
    return error;
}
void Controller::copyText(const QString &text) { QGuiApplication::clipboard()->setText(text); }
