#include "controller.h"
#include "usage.h"
#include <QClipboard>
#include <QGuiApplication>
#include <QDateTime>
#include <QNetworkProxy>
#include <QUrl>
#include <utility>

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
    m_poll.setSingleShot(true);
    m_poll.setTimerType(Qt::PreciseTimer);
    connect(&m_poll, &QTimer::timeout, this, &Controller::refresh);
    if (m_startPolling) m_poll.start(m_interval * 1000);
    connect(&m_clock, &QTimer::timeout, this, [this] { updateMeterStates(); emit changed(); });
    if (m_startPolling) m_clock.start(30000);
    m_localNetwork.setProxy(QNetworkProxy::NoProxy);
    m_server.configure(m_mode, m_token);
    syncConnection();
    connect(&m_credentials, &CredentialService::event, this, [this](const QString &message) {
        log(QStringLiteral("Credentials"), message);
    });
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
        m_status = "offline"; m_message = message; m_errorKind = kind;
        log("Local server", message); emit changed();
    });
    connect(&m_server, &ManagedServer::connectionChanged, this, [this] {
        if (m_mode != QStringLiteral("local")) return;
        cancelResetRequest();
        cancel();
        m_localNetwork.clearConnectionCache();
        syncConnection();
        emit changed();
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
    return {{"mode", m_mode}, {"url", m_url}, {"sshUrl", m_sshUrl}, {"hasToken", !m_token.isEmpty()}, {"interval", m_interval},
        {"notifications", m_notifications}, {"primary", primary()}};
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
    return m_mode == "local" ? m_server.connection().certificate : QSslCertificate();
}
void Controller::syncConnection() {
    if (m_mode == QStringLiteral("local")) {
        const auto transport = m_server.connection();
        m_credentials.configure(m_mode, transport.url.toString(), QString::fromUtf8(transport.token), transport.certificate);
    } else {
        m_credentials.configure(m_mode, backendUrl(), backendToken(), QSslCertificate());
    }
}
QString Controller::displayName(const QString &provider) const { return Usage::displayName(provider); }

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
    if (m_startPolling) m_poll.start(m_interval * 1000);
}
void Controller::fail(const QString &message, const QString &kind) {
    m_status = "offline"; m_message = message; m_loading = false; m_errorKind = kind;
    m_retryAttempt = qMin(m_retryAttempt + 1, 8);
    const int seconds = qMin(m_interval * (1 << m_retryAttempt), qMax(300, m_interval));
    if (m_startPolling) m_poll.start(seconds * 1000);
    log("Connection", message + QString(" Retry in %1 seconds.").arg(seconds));
    emit changed();
}
void Controller::refresh() {
    if (m_loading || m_resetBusy) return;
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
    const QString baseUrl = backendUrl();
    const auto url = Usage::endpoint(baseUrl);
    if (url.isEmpty()) { fail("Enter a valid HTTP or HTTPS backend address in settings."); return; }
    QNetworkRequest request(url);
    request.setRawHeader("Accept", "application/json");
    request.setRawHeader("User-Agent", "Headroom/" HEADROOM_VERSION);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(10000);
    const ServerConnection transport = m_mode == QStringLiteral("local")
        ? m_server.connection() : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), QSslCertificate()};
    if (!transport.token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + transport.token);
    if (m_mode == QStringLiteral("local")) ServerTransport::secureRequest(request, transport.certificate);
    m_loading = true; log("Connection", "Requesting usage snapshot."); emit changed();
    QNetworkAccessManager *network = m_mode == QStringLiteral("local") ? &m_localNetwork
        : m_mode == QStringLiteral("ssh") ? static_cast<QNetworkAccessManager *>(&m_sshNetwork) : &m_network;
    auto reply = network->get(request); m_reply = reply;
    if (m_mode == QStringLiteral("local")) ServerTransport::requirePinnedPeer(reply, transport.certificate);
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(m_mode == QStringLiteral("ssh") ? 27000 : 12000);
    connect(reply, &QNetworkReply::readyRead, this, [reply] { if (reply->bytesAvailable() > 1024 * 1024) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply] {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
        const auto error = reply->error(); const auto body = reply->readAll();
        reply->deleteLater(); m_reply.clear(); m_loading = false;
        if (redirected) { fail("The backend redirected the request. Enter its final address in settings.", "api"); return; }
        if (status == 401 || status == 403) { fail("Your backend rejected the token. Update it in connection settings.", "auth"); return; }
        if (status && status != 200) { fail(QString("Backend returned HTTP %1. Check the address and try again.").arg(status), "api"); return; }
        if (error != QNetworkReply::NoError) {
            if (m_mode == "local") {
                m_waitingForUsageRetry = true;
                m_server.reportConnectionFailure();
                fail("Cannot reach the local usage server. Headroom will recheck it before retrying.");
            } else if (m_mode == "ssh") fail("Cannot connect over SSH. Make sure OpenSSH is installed, then check the address, trusted host key, and key or agent authentication.");
            else fail("Cannot reach your backend. Check your network and connection settings.");
            return;
        }
        QVariantList providers;
        if (body.size() > 1024 * 1024 || !Usage::parse(body, providers)) { fail("The backend returned an unexpected usage response.", "malformed"); return; }
        acceptSnapshot(providers);
    });
}
void Controller::acceptSnapshot(const QVariantList &providers) {
    resetRetry();
    m_waitingForUsageRetry = false;
    int failed = 0;
    for (const auto &provider : providers) if (!provider.toMap()["is_success"].toBool()) ++failed;
    log("Connection", QString("Snapshot received: %1 providers, %2 unavailable.").arg(providers.size()).arg(failed));
    m_providers = providers; m_lastGood = QDateTime::currentSecsSinceEpoch(); m_status = "ready"; m_message.clear();
    observeResetUsage();
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
}
Controller::~Controller() {
    // The network manager outlives every other member, so an in-flight reply must be
    // disconnected and aborted before the members its handler touches are destroyed.
    cancel();
    cancelResetRequest();
}
QString Controller::saveSettings(QString mode, QString url, QString token, int interval, bool notifications, QString primary, bool forgetToken, QString sshUrl) {
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
    const QString error = writeSettings(mode, url, savedToken, retainedSshUrl, interval, notifications, primary);
    if (!error.isEmpty()) return error;
    cancel();
    m_waitingForUsageRetry = false;
    if (m_mode != mode || m_url != url || m_token != savedToken || m_sshUrl != retainedSshUrl) { m_providers.clear(); m_lastGood = 0; m_warningStates.clear(); m_concerns.clear(); m_notificationCenter.resetBankedResetBaseline(); }
    m_mode = mode; m_url = url; m_token = savedToken; m_sshUrl = retainedSshUrl; m_interval = interval; m_notifications = notifications; m_primary = primary;
    m_server.configure(m_mode, m_token);
    syncConnection();
    m_status = "connecting"; m_message.clear(); resetRetry();
    log("Settings", "Connection settings saved.");
    emit settingsChanged(); emit providersChanged(); emit changed(); refresh(); return {};
}
QString Controller::writeSettings(const QString &mode, const QString &url, const QString &token, const QString &sshUrl, int interval, bool notifications, const QString &primary) {
    DesktopSettings updated = m_settingsService.value();
    updated.connectionMode = mode; updated.url = url; updated.token = token;
    updated.sshUrl = sshUrl;
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
        emit notify("Order could not be saved", error); return;
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
                emit usageAlert(title, message, int(transition.to));
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
