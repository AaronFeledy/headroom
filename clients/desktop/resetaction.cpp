#include "controller.h"
#include "usage.h"
#include "resetpolicy.h"
#include <QCryptographicHash>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>
#include <QRegularExpression>
#include <QUuid>
#include <cmath>
#include <cstring>
#include <utility>

// IMPORTANT: DO NOT test the reset button, its endpoint, or any code that might
// trigger a reset. It can burn a very valuable banked reset. The corresponding
// skipped tests are deliberate safeguards, not missing tests to implement.
namespace {
// DO NOT test this upload through a reset path. It is deliberately sequential
// and cannot rewind: Qt must fail a resend instead of replaying a redemption.
class ResetUpload final : public QIODevice {
public:
    explicit ResetUpload(QByteArray body, QObject *parent) : QIODevice(parent), m_body(std::move(body)) {
        open(QIODevice::ReadOnly | QIODevice::Unbuffered);
    }
    bool isSequential() const override { return true; }
    bool reset() override { return false; }
    bool seek(qint64) override { return false; }
    qint64 bytesAvailable() const override { return m_body.size() - m_offset + QIODevice::bytesAvailable(); }
protected:
    qint64 readData(char *data, qint64 maxSize) override {
        const qint64 count = qMin(maxSize, qint64(m_body.size()) - m_offset);
        if (count <= 0) return maxSize == 0 ? 0 : -1;
        std::memcpy(data, m_body.constData() + m_offset, size_t(count));
        m_offset += count;
        return count;
    }
    qint64 writeData(const char *, qint64) override { return -1; }
private:
    QByteArray m_body;
    qint64 m_offset = 0;
};

bool validFingerprint(const QString &value) {
    static const QRegularExpression pattern(QStringLiteral("^[0-9a-f]{64}$"));
    return value.size() == 64 && pattern.match(value).hasMatch();
}

QString canonicalWindow(const QVariantMap &weekly) {
    const auto reset = QDateTime::fromString(weekly.value("resets_at").toString(), Qt::ISODateWithMs);
    return reset.isValid() ? reset.toUTC().toString(Qt::ISODateWithMs) : QString();
}

ResetPolicy::Window windowState(const QString &canonical) {
    const auto deadline = QDateTime::fromString(canonical, Qt::ISODateWithMs);
    if (!deadline.isValid()) return ResetPolicy::Window::Unknown;
    return deadline > QDateTime::currentDateTimeUtc() ? ResetPolicy::Window::Future : ResetPolicy::Window::Ended;
}

ResetPolicy::Provider providerKind(const QString &provider) {
    return provider == "Claude" ? ResetPolicy::Provider::Claude : ResetPolicy::Provider::Codex;
}

QString receiptPrefix(const QString &settingsPath, const QString &provider) {
    // Preserve old Codex receipt paths. Claude has a separate provider namespace.
    return settingsPath + (provider == "Claude" ? ".reset-claude-account-" : ".reset-account-");
}

bool readReceipt(const QString &path, QJsonObject &receipt) {
    if (!QFileInfo::exists(path)) return true;
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly) || file.size() > 4096) return false;
    QJsonParseError error;
    const auto document = QJsonDocument::fromJson(file.readAll(), &error);
    receipt = document.object();
    return error.error == QJsonParseError::NoError && document.isObject()
        && !QUuid(receipt.value("request_id").toString()).isNull()
        && receipt.value("completed").isBool();
}
bool writeReceipt(const QString &path, const QJsonObject &receipt) {
    if (!QDir().mkpath(QFileInfo(path).absolutePath())) return false;
    QSaveFile file(path);
    if (!file.open(QIODevice::WriteOnly)) return false;
    if (!file.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner)) return false;
    const auto bytes = QJsonDocument(receipt).toJson(QJsonDocument::Compact);
    return file.write(bytes) == bytes.size() && file.commit();
}
}

QVariantMap Controller::resetReading(const QString &name) const {
    if (name != "Codex" && name != "Claude") return {};
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        if (provider.value("provider_name").toString() != name || !provider.value("is_success").toBool()) continue;
        auto reading = provider.value("rate_limit_reset_credits").toMap();
        reading.insert("fetched_at", provider.value("fetch_status").toMap().value("fetched_at"));
        double utilization = 0;
        for (const auto &bucket : provider.value("buckets").toList()) {
            const auto meter = bucket.toMap();
            const auto id = meter.value("id").toString();
            if (id == "weekly") reading.insert("resets_at", meter.value("resets_at"));
            if (id == "weekly" || (name == "Claude" && id == "session")) {
                bool valid = false;
                const double used = meter.value("utilization").toDouble(&valid);
                if (valid && std::isfinite(used)) utilization = qMax(utilization, used);
            }
        }
        if (name == "Codex" && !reading.contains("resets_at")) return {};
        reading.insert("utilization", utilization);
        return reading;
    }
    return {};
}

bool Controller::resetReadingFresh(const QString &provider) const {
    const auto fetchedAt = QDateTime::fromString(resetReading(provider).value("fetched_at").toString(), Qt::ISODateWithMs);
    const qint64 now = QDateTime::currentSecsSinceEpoch();
    const int maximumAge = qMax(120, m_interval * 2);
    // WHY: server fetched_at is not comparable to the desktop clock. Like Codex,
    // desktop freshness uses m_lastGood; the server re-polls Claude and re-reads
    // banked status before spending. A valid fetched_at anchors the receipt.
    return m_status == "ready" && !m_loading && m_lastGood > 0 && now - m_lastGood <= maximumAge
        && (provider != "Claude" || fetchedAt.isValid());
}

ResetPolicy::Facts Controller::resetFacts(const QString &provider) const {
    // Facts only: the pure policy decides what they permit. Unknown values
    // stay unknown rather than defaulting to a permissive reading.
    ResetPolicy::Facts facts;
    facts.provider = providerKind(provider);
    const auto reading = resetReading(provider);
    if (provider == "Codex" || provider == "Claude") {
        for (const auto &value : m_providers) {
            const auto current = value.toMap();
            if (current.value("provider_name").toString() != provider) continue;
            facts.readingAvailable = current.value("is_success").toBool();
            break;
        }
    }
    facts.fresh = resetReadingFresh(provider);
    if (reading.contains("available_count")) facts.availableCount = reading.value("available_count").toLongLong();
    bool validUsage = false;
    const double utilization = reading.value("utilization").toDouble(&validUsage);
    if (facts.readingAvailable && validUsage) facts.utilization = utilization;
    if (reading.value("usable_now").metaType().id() == QMetaType::Bool) facts.usableNow = reading.value("usable_now").toBool();
    facts.accountBound = validFingerprint(reading.value("account_fingerprint").toString());
    const QString window = canonicalWindow(reading);
    facts.window = windowState(window);
    // Keep an unverifiable pending request visible for its provider, without
    // treating missing account metadata as permission or proof of recovery.
    facts.awaitingUsage = resetAwaitingUsage(provider) || (!facts.accountBound
        && (provider == "Codex" || provider == "Claude")
        && m_resetBlockedReceipt.startsWith(receiptPrefix(m_settingsService.path(), provider)));
    facts.busy = m_resetBusy;
    facts.owner = provider == m_resetProvider;
    facts.confirmed = !m_resetConfirmation.isEmpty();
    facts.confirmationCurrent = facts.confirmed && m_resetConfirmation == resetConnectionIdentity();
    facts.armed = !m_autoResetConnection.isEmpty();
    facts.armIdentityCurrent = facts.armed && m_autoResetConnection == resetConnectionIdentity();
    facts.armWindowCurrent = facts.armed && window == m_autoResetWindow;
    facts.armWindow = windowState(m_autoResetWindow);
    return facts;
}

bool Controller::resetAwaitingUsage(const QString &provider) const {
    const QString path = resetReceiptPath(provider);
    // Any submitted request stays latched, including an uncertain result or
    // an app restart. Never offer another spend while usage is still >= 90%.
    return !path.isEmpty() && (m_resetBlockedReceipt == path || QFileInfo::exists(path)
        || (provider == "Codex" && !legacyResetReceiptPaths().isEmpty()));
}

QString Controller::resetConnectionIdentity() const {
    // Confirmation is invalidated by a connection, token, or pinned-peer change.
    const auto bytes = m_mode.toUtf8() + '\0' + backendUrl().toUtf8() + '\0'
        + backendToken().toUtf8() + '\0' + backendCertificate().toDer() + '\0'
        + m_resetProvider.toUtf8() + '\0'
        + resetReading(m_resetProvider).value("account_fingerprint").toString().toUtf8();
    return QString::fromLatin1(QCryptographicHash::hash(bytes, QCryptographicHash::Sha256).toHex());
}

QString Controller::resetReceiptPath(const QString &provider) const {
    // The opaque server-derived fingerprint keeps one redemption identity for
    // an account across local launches, transports, and equivalent addresses.
    const QString fingerprint = resetReading(provider).value("account_fingerprint").toString();
    if (!validFingerprint(fingerprint)) return {};
    return receiptPrefix(m_settingsService.path(), provider) + fingerprint + ".json";
}

QStringList Controller::legacyResetReceiptPaths() const {
    // Detect every receipt written before account binding, including one made
    // through a different transport or address. None can be safely adopted
    // because its UUID may already have reached another account.
    const QFileInfo settings(m_settingsService.path());
    const QString prefix = settings.fileName() + QStringLiteral(".reset-");
    QStringList paths;
    for (const auto &entry : QDir(settings.absolutePath()).entryInfoList(QDir::Files | QDir::NoDotAndDotDot)) {
        const QString name = entry.fileName();
        if (!name.startsWith(prefix) || !name.endsWith(QStringLiteral(".json"))) continue;
        const QString key = name.mid(prefix.size(), name.size() - prefix.size() - QStringLiteral(".json").size());
        if (validFingerprint(key)) paths.append(entry.absoluteFilePath());
    }
    return paths;
}

QVariantMap Controller::resetState(const QString &provider) const {
    const auto facts = resetFacts(provider);
    const auto capabilities = ResetPolicy::evaluate(facts);
    using ResetPolicy::Phase;
    const bool armed = capabilities.phase == Phase::Armed || capabilities.phase == Phase::Paused;
    return {{"automatic", armed}, {"awaitingUsage", facts.awaitingUsage}, {"enabled", capabilities.canOpen},
        {"eligible", capabilities.canUseNow}, {"visible", capabilities.visible},
        {"managing", capabilities.visible && capabilities.phase != Phase::Choices},
        {"phase", ResetPolicy::phaseName(capabilities.phase)},
        {"openReason", ResetPolicy::reasonText(capabilities.openReason, facts.provider)}};
}

QVariantMap Controller::resetAction() const {
    const auto facts = resetFacts(m_resetProvider);
    const auto capabilities = ResetPolicy::evaluate(facts);
    const bool armed = !m_autoResetConnection.isEmpty();
    return {{"provider", m_resetProvider}, {"busy", m_resetBusy}, {"message", m_resetMessage}, {"automatic", armed},
        {"availableCount", facts.availableCount ? QVariant::fromValue(*facts.availableCount) : QVariant()},
        {"awaitingUsage", facts.awaitingUsage},
        {"enabled", armed || (m_resetConfirmation.isEmpty() && capabilities.canUseNow)},
        {"phase", ResetPolicy::phaseName(capabilities.phase)},
        {"canConfirm", capabilities.canConfirm}, {"canSchedule", capabilities.canSchedule},
        {"canCancel", capabilities.canCancelArm},
        {"useNowReason", ResetPolicy::reasonText(capabilities.useNowReason, facts.provider)},
        {"automaticReason", ResetPolicy::reasonText(capabilities.armReason, facts.provider)},
        {"windowEndsAt", armed ? m_autoResetWindow : canonicalWindow(resetReading(m_resetProvider))}};
}

bool Controller::prepareReset(const QString &provider) {
    // Opens one provider's options. The policy alone grants permission to
    // use or arm; this binds the options to the current account/connection
    // identity and explains a pending receipt. It never sends a request.
    if (provider != "Codex" && provider != "Claude") return false;
    if (!ResetPolicy::evaluate(resetFacts(provider)).canOpen) return false;
    m_resetProvider = provider;
    m_resetConfirmation.clear();
    m_resetMessage.clear();
    const QString fingerprint = resetReading(provider).value("account_fingerprint").toString();
    const QString receiptPath = resetReceiptPath(provider);
    QJsonObject receipt;
    if (!readReceipt(receiptPath, receipt)) {
        m_resetMessage = QString("The previous reset request could not be read safely. Check your usage in %1.").arg(displayName(provider));
    } else if (!receipt.isEmpty() && (receipt.value("account_fingerprint").toString() != fingerprint
        || receipt.value("provider").toString("Codex") != provider)) {
        m_resetMessage = QString("The previous reset request is not bound to this %1 account. Check your usage in %1.").arg(displayName(provider));
    }
    for (const QString &legacyPath : provider == "Codex" && m_resetMessage.isEmpty() ? legacyResetReceiptPaths() : QStringList{}) {
        QJsonObject legacy;
        if (!readReceipt(legacyPath, legacy)) {
            m_resetMessage = "The previous reset request could not be read safely. Check your usage in ChatGPT.";
        } else if (!legacy.value("completed").toBool()) {
            m_resetMessage = "A previous reset request is not bound to a ChatGPT account and cannot be retried safely. Check it in ChatGPT.";
        } else {
            m_resetBlockedReceipt = legacyPath;
            m_resetMessage = "A reset was already used. Waiting for updated weekly usage.";
        }
        break;
    }
    if (m_resetMessage.isEmpty() && receipt.value("completed").toBool()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = provider == "Claude" ? "A reset was already used. Waiting for updated Claude usage."
            : "A reset was already used. Waiting for updated weekly usage.";
    } else if (m_resetMessage.isEmpty() && !receipt.isEmpty()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = ResetPolicy::awaitingUsageText(providerKind(provider), false);
    }
    // The confirmation identity binds these options to the account and
    // connection they were opened for; permission still comes from the policy.
    m_resetConfirmation = resetConnectionIdentity();
    emit changed();
    return true;
}

void Controller::cancelResetConfirmation() {
    m_resetConfirmation.clear(); emit changed();
}

bool Controller::scheduleReset() {
    // This button explicitly authorizes ONE future submission. DO NOT invoke
    // it during testing, even with a mock transport or synthetic usage.
    if (!resetAction().value("canSchedule").toBool()) return false;
    m_autoResetConnection = m_resetConfirmation;
    m_autoResetWindow = canonicalWindow(resetReading(m_resetProvider));
    m_resetConfirmation.clear();
    m_resetMessage.clear();
    // Read the server's usage cache promptly without changing its provider
    // polling cadence. Submission happens only after an accepted snapshot.
    if (m_startPolling) m_poll.start(0);
    emit changed();
    return true;
}

void Controller::clearScheduledReset() {
    m_autoResetConnection.clear();
    m_autoResetWindow.clear();
    if (m_startPolling && m_poll.isActive()) m_poll.start(m_interval * 1000);
}

void Controller::cancelScheduledReset() {
    if (m_autoResetConnection.isEmpty()) return;
    clearScheduledReset();
    m_resetMessage = ResetPolicy::cancellationText(ResetPolicy::Reason::None);
    emit changed();
}

void Controller::retireScheduledReset(ResetPolicy::Reason reason) {
    // Disarm without I/O and say why. Nothing was requested.
    clearScheduledReset();
    m_resetMessage = ResetPolicy::cancellationText(reason);
    if (m_notifications) m_notificationCenter.post("providerCard_" + m_resetProvider, displayName(m_resetProvider) + " · Automatic reset", m_resetMessage);
    emit changed();
}

void Controller::expireScheduledReset() {
    // Time-based disarm only, so the periodic clock can retire an arm whose
    // weekly window ended while the backend was unreachable. Submission still
    // requires an accepted snapshot: this never reads usage or performs I/O.
    if (m_autoResetConnection.isEmpty() || m_resetBusy) return;
    const auto expiry = ResetPolicy::armExpiry(windowState(m_autoResetWindow));
    if (expiry != ResetPolicy::Reason::None) retireScheduledReset(expiry);
}

void Controller::observeScheduledReset() {
    // DO NOT TEST: this path can spend a reset after explicit one-shot consent.
    // Unarmed polling remains read-only. Errors never imply exhaustion.
    if (m_autoResetConnection.isEmpty() || m_resetBusy) return;
    // The policy decides: an ended window retires the arm even on a late
    // snapshot, a provider error pauses, a changed identity or window, a
    // reading below the floor, no credits, or a pending request cancels.
    const auto decision = ResetPolicy::observeArm(resetFacts(m_resetProvider));
    if (decision.outcome == ResetPolicy::ArmOutcome::Cancel) { retireScheduledReset(decision.reason); return; }
    if (decision.outcome != ResetPolicy::ArmOutcome::Submit) return;
    const QString authorization = m_autoResetConnection;
    // Disarm before any I/O, including receipt creation. A failure
    // or ambiguous result must never schedule another attempt.
    clearScheduledReset();
    m_resetConfirmation = authorization;
    // Claude also requires usable_now == true. DO NOT TEST or invoke this path.
    submitReset(true);
    if (!m_resetBusy && m_notifications && !m_resetMessage.isEmpty())
        m_notificationCenter.post("providerCard_" + m_resetProvider, displayName(m_resetProvider) + " · Automatic reset", m_resetMessage);
}

void Controller::observeResetUsage() {
    // Read-only usage polling never sends a redemption. Claude receipts need
    // durable server evidence for their exact account and request UUID. Usage
    // timestamps and desktop count baselines cannot establish that evidence.
    if (m_resetBusy) return;
    for (const QString provider : {QStringLiteral("Codex"), QStringLiteral("Claude")}) {
        const auto weekly = resetReading(provider);
        if (weekly.isEmpty()) continue;
        const QString path = resetReceiptPath(provider);
        QJsonObject receipt;
        if (path.isEmpty() || !readReceipt(path, receipt) || receipt.isEmpty()
            || receipt.value("provider").toString("Codex") != provider
            || receipt.value("account_fingerprint").toString() != weekly.value("account_fingerprint").toString()) continue;
        bool validUsage = false;
        const double utilization = weekly.value("utilization").toDouble(&validUsage);
        const QString requestID = QUuid(receipt.value("request_id").toString()).toString(QUuid::WithoutBraces);
        const bool released = provider == "Claude" ? weekly.value("reconciled_request_ids").toStringList().contains(requestID)
            : ResetPolicy::releasesCodexReceipt(validUsage ? std::optional<double>(utilization) : std::nullopt);
        if (released && QFile::remove(path)) {
            if (m_resetBlockedReceipt == path) m_resetBlockedReceipt.clear();
            if (m_resetProvider == provider) m_resetMessage = provider == "Claude" ? "Claude usage has been updated." : "Weekly usage has been updated.";
        }
    }
    // Legacy receipts have no account identity. Even a completed receipt may
    // belong to another account, so this reading cannot safely release it.
}

void Controller::cancelResetRequest() {
    m_resetConfirmation.clear();
    if (!m_resetReply) return;
    const bool automatic = m_resetReply->property("automaticReset").toBool();
    disconnect(m_resetReply, nullptr, this, nullptr);
    m_resetReply->abort(); m_resetReply->deleteLater(); m_resetReply.clear();
    m_resetBusy = false;
    m_resetMessage = ResetPolicy::awaitingUsageText(providerKind(m_resetProvider), true);
    if (automatic && m_notifications)
        m_notificationCenter.post("providerCard_" + m_resetProvider, displayName(m_resetProvider) + " · Automatic reset", m_resetMessage);
    // Leave the durable receipt intact, including when the app quits mid-request.
}

void Controller::consumeReset() {
    // DO NOT TEST OR INVOKE for validation: this spends a valuable real reset.
    // The explicit Use now button supersedes any pending automatic choice.
    if (!resetAction().value("canConfirm").toBool()) return;
    clearScheduledReset();
    submitReset(false);
}

void Controller::submitReset(bool automatic) {
    // Only explicit immediate or one-shot scheduled consent reaches here.
    // Never retry automatically, follow redirects, or offer another request
    // until read-only usage has released the durable account latch.
    if (!resetAction().value("canConfirm").toBool()) return;
    // DO NOT TEST: both Codex and Claude submissions spend real banked resets.
    const QString receiptPath = resetReceiptPath(m_resetProvider);
    const auto weekly = resetReading(m_resetProvider);
    const QString fingerprint = weekly.value("account_fingerprint").toString();
    QJsonObject receipt;
    if (!readReceipt(receiptPath, receipt) || !receipt.isEmpty()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = QString("Check your previous reset in %1 before continuing.").arg(displayName(m_resetProvider));
        m_resetConfirmation.clear(); emit changed(); return;
    }
    if (m_resetProvider == "Codex" && !legacyResetReceiptPaths().isEmpty()) {
        m_resetMessage = "A previous reset request is not bound to a ChatGPT account and cannot be retried safely. Check it in ChatGPT.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    const QString requestWindow = canonicalWindow(weekly);
    receipt = {{"request_id", QUuid::createUuid().toString(QUuid::WithoutBraces)},
        {"completed", false}, {"account_fingerprint", fingerprint}, {"provider", m_resetProvider},
        {"available_count", weekly.value("available_count").toLongLong()},
        {"submitted_at", QDateTime::currentDateTimeUtc().toString(Qt::ISODateWithMs)},
        {"reading_fetched_at", weekly.value("fetched_at").toString()},
        {"weekly_resets_at", requestWindow.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(requestWindow)}};
    if (!writeReceipt(receiptPath, receipt)) {
        m_resetMessage = "The reset request could not be saved safely. No reset was requested.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    auto url = Usage::endpoint(backendUrl());
    QString path = url.path();
    path.chop(QStringLiteral("usage").size());
    url.setPath(path + QStringLiteral("providers/") + m_resetProvider.toLower() + QStringLiteral("/reset"));
    QNetworkRequest request(url);
    request.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    request.setRawHeader("Accept", "application/json");
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(95000);
    const ServerConnection transport = m_mode == "local" ? m_server.connection()
        : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), backendCertificate()};
    if (!transport.token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + transport.token);
    ServerTransport::secureRequest(request, transport.certificate, m_mode == QStringLiteral("remote"));
    const auto body = QJsonDocument(QJsonObject{{"request_id", receipt.value("request_id")},
        {"confirmed", true}, {"account_fingerprint", fingerprint}}).toJson(QJsonDocument::Compact);
    request.setHeader(QNetworkRequest::ContentLengthHeader, qint64(body.size()));
    request.setAttribute(QNetworkRequest::DoNotBufferUploadDataAttribute, true);
    request.setAttribute(QNetworkRequest::Http2AllowedAttribute, false);
    // Latch synchronously before POST, not after a response: rapid clicks,
    // errors, cancellation, and restarts must not allow a second spend.
    m_resetBlockedReceipt = receiptPath;
    cancel(); m_poll.stop(); m_resetBusy = true; m_resetConfirmation.clear(); m_resetMessage = "Using one banked reset…";
    emit changed();
    QNetworkAccessManager *network = m_mode == "local" ? &m_localNetwork
        : m_mode == "ssh" ? static_cast<QNetworkAccessManager *>(&m_sshNetwork) : &m_network;
    auto upload = new ResetUpload(body, this);
    auto reply = network->post(request, upload); m_resetReply = reply;
    reply->setProperty("automaticReset", automatic);
    upload->setParent(reply);
    ServerTransport::requirePinnedPeer(reply, transport.certificate, m_mode == QStringLiteral("remote"));
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(100000);
    connect(reply, &QNetworkReply::readyRead, this, [reply] { if (reply->bytesAvailable() > 4096) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply, receiptPath, receipt, requestWindow, automatic]() mutable {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
        const auto error = reply->error(); const auto bytes = reply->readAll();
        reply->deleteLater(); m_resetReply.clear(); m_resetBusy = false;
        const auto outcome = bytes.size() <= 4096 ? QJsonDocument::fromJson(bytes).object().value("outcome").toString() : QString();
        const bool terminal = outcome == "reset" || outcome == "already_redeemed" || outcome == "nothing_to_reset" || outcome == "no_credit" || outcome == "unavailable";
        if (!redirected && error == QNetworkReply::NoError && status == 200 && terminal) {
            // Retain the submitted window as receipt metadata, never as a
            // reason to release the latch while usage remains at or above 90%.
            if (outcome == "reset") receipt.insert("weekly_resets_at",
                requestWindow.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(requestWindow));
            receipt.insert("completed", true);
            writeReceipt(receiptPath, receipt); // The original receipt keeps the latch if this write fails.
            m_resetBlockedReceipt = receiptPath;
            if (outcome == "reset") m_resetMessage = "One banked reset was used. Refreshing usage…";
            else if (outcome == "already_redeemed") m_resetMessage = "This reset request was already completed. Refreshing usage…";
            else if (outcome == "no_credit") m_resetMessage = "No banked resets are available. Refreshing usage…";
            else if (outcome == "unavailable") m_resetMessage = "Anthropic didn't apply a reset right now; no reset was used. Refreshing usage…";
            else m_resetMessage = "There is no usage to reset. Refreshing usage…";
        } else if (status == 404) m_resetMessage = QString("Update your usage server to use resets here, or open %1's usage page.").arg(displayName(m_resetProvider));
        else if (status == 409) m_resetMessage = QString("The server could not safely proceed. Refresh usage or check the previous reset in %1.").arg(displayName(m_resetProvider));
        else if (status == 401 || status == 403) m_resetMessage = QString("The reset request was rejected. Check your connection and %1 sign-in.").arg(displayName(m_resetProvider));
        else m_resetMessage = ResetPolicy::awaitingUsageText(providerKind(m_resetProvider), true);
        if (automatic && m_notifications)
            m_notificationCenter.post("providerCard_" + m_resetProvider, displayName(m_resetProvider) + " · Automatic reset", m_resetMessage);
        emit changed();
        // DO NOT test these callbacks as part of the redemption flow. They
        // only GET usage; they must never repeat or confirm a reset request.
        // The server refetches OpenAI after three seconds. These bounded reads
        // allow for propagation/network delay, then normal polling takes over.
        const QString connection = resetConnectionIdentity();
        if (m_startPolling) m_poll.start(m_interval * 1000);
        for (const int delay : {5000, 12000, 25000}) {
            QTimer::singleShot(delay, this, [this, connection] {
                if (resetAwaitingUsage(m_resetProvider) && resetConnectionIdentity() == connection) refresh();
            });
        }
    });
}
