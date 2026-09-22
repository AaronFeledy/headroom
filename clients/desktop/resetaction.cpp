#include "controller.h"
#include "usage.h"
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

QVariantMap Controller::chatGptWeekly() const {
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        if (provider.value("provider_name").toString() != "Codex" || !provider.value("is_success").toBool()) continue;
        for (const auto &bucket : provider.value("buckets").toList()) {
            auto weekly = bucket.toMap();
            if (weekly.value("id").toString() != "weekly") continue;
            const auto credits = provider.value("rate_limit_reset_credits").toMap();
            weekly.insert("available_count", credits.value("available_count"));
            weekly.insert("account_fingerprint", credits.value("account_fingerprint"));
            return weekly;
        }
    }
    return {};
}

bool Controller::chatGptResetEligible() const {
    const auto weekly = chatGptWeekly();
    return m_status == "ready" && !m_loading && !m_resetBusy && m_lastGood > 0
        && QDateTime::currentSecsSinceEpoch() - m_lastGood <= qMax(120, m_interval * 2)
        && weekly.value("utilization").toDouble() >= 95
        && weekly.value("available_count").toDouble() > 0
        && validFingerprint(weekly.value("account_fingerprint").toString())
        && !chatGptResetAwaitingUsage();
}

bool Controller::chatGptResetAwaitingUsage() const {
    const QString path = resetReceiptPath();
    // Any submitted request stays latched, including an uncertain result or
    // an app restart. Never offer another spend while usage is still >= 95%.
    return !path.isEmpty() && (m_resetBlockedReceipt == path || QFileInfo::exists(path)
        || !legacyResetReceiptPaths().isEmpty());
}

QString Controller::resetConnectionIdentity() const {
    // Confirmation is invalidated by a connection, token, or pinned-peer change.
    const auto bytes = m_mode.toUtf8() + '\0' + backendUrl().toUtf8() + '\0'
        + backendToken().toUtf8() + '\0' + backendCertificate().toDer() + '\0'
        + chatGptWeekly().value("account_fingerprint").toString().toUtf8();
    return QString::fromLatin1(QCryptographicHash::hash(bytes, QCryptographicHash::Sha256).toHex());
}

QString Controller::resetReceiptPath() const {
    // The opaque server-derived fingerprint keeps one redemption identity for
    // an account across local launches, transports, and equivalent addresses.
    const QString fingerprint = chatGptWeekly().value("account_fingerprint").toString();
    if (!validFingerprint(fingerprint)) return {};
    return m_settingsService.path() + ".reset-account-" + fingerprint + ".json";
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

QVariantMap Controller::resetAction() const {
    const bool automatic = !m_autoResetConnection.isEmpty();
    const bool canConfirm = !m_resetConfirmation.isEmpty()
        && m_resetConfirmation == resetConnectionIdentity() && chatGptResetEligible();
    const auto windowEnd = QDateTime::fromString(canonicalWindow(chatGptWeekly()), Qt::ISODateWithMs);
    return {{"busy", m_resetBusy}, {"message", m_resetMessage}, {"automatic", automatic},
        {"awaitingUsage", chatGptResetAwaitingUsage()},
        {"enabled", automatic || (m_resetConfirmation.isEmpty() && chatGptResetEligible())},
        {"canConfirm", canConfirm},
        {"canSchedule", canConfirm && !automatic && windowEnd.isValid() && windowEnd > QDateTime::currentDateTimeUtc()}};
}

bool Controller::prepareChatGptReset() {
    m_resetConfirmation.clear();
    m_resetMessage.clear();
    if (!chatGptResetEligible()) {
        m_resetMessage = chatGptResetAwaitingUsage()
            ? "A reset has already been requested. Waiting for weekly usage to drop below 95%."
            : "Refresh usage before using a reset. Weekly usage must be at least 95% and a banked reset must be available.";
        emit changed(); return false;
    }
    const auto weekly = chatGptWeekly();
    const QString fingerprint = weekly.value("account_fingerprint").toString();
    const QString receiptPath = resetReceiptPath();
    QJsonObject receipt;
    if (!readReceipt(receiptPath, receipt)) {
        m_resetMessage = "The previous reset request could not be read safely. Check your usage in ChatGPT.";
        emit changed(); return false;
    }
    if (!receipt.isEmpty() && receipt.value("account_fingerprint").toString() != fingerprint) {
        m_resetMessage = "The previous reset request is not bound to this ChatGPT account. Check your usage in ChatGPT.";
        emit changed(); return false;
    }
    for (const QString &legacyPath : legacyResetReceiptPaths()) {
        QJsonObject legacy;
        if (!readReceipt(legacyPath, legacy)) {
            m_resetMessage = "The previous reset request could not be read safely. Check your usage in ChatGPT.";
        } else if (!legacy.value("completed").toBool()) {
            m_resetMessage = "A previous reset request is not bound to a ChatGPT account and cannot be retried safely. Check it in ChatGPT.";
        } else {
            m_resetBlockedReceipt = legacyPath;
            m_resetMessage = "A reset was already used. Waiting for updated weekly usage.";
        }
        emit changed(); return false;
    }
    if (receipt.value("completed").toBool()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = "A reset was already used. Waiting for updated weekly usage.";
        emit changed(); return false;
    }
    if (!receipt.isEmpty()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = "A reset has already been requested. Waiting for weekly usage to drop below 95%.";
        emit changed(); return false;
    }
    m_resetConfirmation = resetConnectionIdentity();
    emit changed(); return true;
}

void Controller::cancelChatGptResetConfirmation() {
    m_resetConfirmation.clear(); emit changed();
}

bool Controller::scheduleChatGptReset() {
    // This button explicitly authorizes ONE future submission. DO NOT invoke
    // it during testing, even with a mock transport or synthetic usage.
    if (!resetAction().value("canSchedule").toBool()) return false;
    m_autoResetConnection = m_resetConfirmation;
    m_autoResetWindow = canonicalWindow(chatGptWeekly());
    m_resetConfirmation.clear();
    m_resetMessage.clear();
    // Read the server's usage cache promptly without changing its provider
    // polling cadence. Submission happens only after an accepted snapshot.
    if (m_startPolling) m_poll.start(0);
    emit changed();
    return true;
}

void Controller::clearScheduledChatGptReset() {
    m_autoResetConnection.clear();
    m_autoResetWindow.clear();
    if (m_startPolling && m_poll.isActive()) m_poll.start(m_interval * 1000);
}

void Controller::cancelScheduledChatGptReset() {
    if (m_autoResetConnection.isEmpty()) return;
    clearScheduledChatGptReset();
    m_resetMessage = "Automatic reset canceled. No reset was requested.";
    emit changed();
}

void Controller::expireScheduledChatGptReset() {
    // Time-based disarm only, so the periodic clock can retire an arm whose
    // weekly window ended while the backend was unreachable. Submission still
    // requires an accepted snapshot: this never reads usage or performs I/O.
    if (m_autoResetConnection.isEmpty() || m_resetBusy) return;
    const auto windowEnd = QDateTime::fromString(m_autoResetWindow, Qt::ISODateWithMs);
    if (windowEnd.isValid() && windowEnd > QDateTime::currentDateTimeUtc()) return;
    const QString cancellation = "Automatic reset canceled because the weekly window ended.";
    clearScheduledChatGptReset();
    m_resetMessage = cancellation;
    if (m_notifications) m_notificationCenter.post("providerCard_Codex", "ChatGPT · Automatic reset", cancellation);
    emit changed();
}

void Controller::observeScheduledChatGptReset() {
    // DO NOT TEST: this path can spend a reset after explicit one-shot consent.
    // Unarmed polling remains read-only. Errors never imply exhaustion.
    if (m_autoResetConnection.isEmpty() || m_resetBusy) return;
    // An ended window retires the arm even when this snapshot is the first in a
    // while; the clock applies the same check when no snapshot arrives at all.
    expireScheduledChatGptReset();
    if (m_autoResetConnection.isEmpty()) return;
    const auto weekly = chatGptWeekly();
    QString cancellation;
    // A temporary provider error pauses monitoring. A successful response
    // with a different/missing identity or window invalidates authorization.
    bool successful = false;
    for (const auto &value : m_providers) {
        const auto provider = value.toMap();
        if (provider.value("provider_name").toString() == "Codex")
            successful = provider.value("is_success").toBool();
    }
    if (!successful) return;
    if (resetConnectionIdentity() != m_autoResetConnection
        || canonicalWindow(weekly) != m_autoResetWindow) {
        cancellation = "Automatic reset canceled because the account, connection, or weekly window changed.";
    } else {
        bool validUsage = false;
        const double utilization = weekly.value("utilization").toDouble(&validUsage);
        if (!validUsage || !std::isfinite(utilization) || utilization < 0) return;
        if (utilization < 95) cancellation = "Automatic reset canceled because weekly usage is below 95%.";
        else if (weekly.value("available_count").toLongLong() <= 0)
            cancellation = "Automatic reset canceled because no banked resets are available.";
        else if (chatGptResetAwaitingUsage())
            cancellation = "Automatic reset canceled because a reset has already been requested.";
        else if (utilization >= 100 && chatGptResetEligible()) {
            const QString authorization = m_autoResetConnection;
            // Disarm before any I/O, including receipt creation. A failure
            // or ambiguous result must never schedule another attempt.
            clearScheduledChatGptReset();
            m_resetConfirmation = authorization;
            submitChatGptReset(true);
            if (!m_resetBusy && m_notifications && !m_resetMessage.isEmpty())
                m_notificationCenter.post("providerCard_Codex", "ChatGPT · Automatic reset", m_resetMessage);
            return;
        }
    }
    if (cancellation.isEmpty()) return;
    clearScheduledChatGptReset();
    m_resetMessage = cancellation;
    if (m_notifications) m_notificationCenter.post("providerCard_Codex", "ChatGPT · Automatic reset", cancellation);
    emit changed();
}

void Controller::observeResetUsage() {
    // Read-only usage polling never sends a redemption. Only a successful
    // reading below 95% releases the account-bound latch. A different reset
    // timestamp, missing/zero credits, an error, or an uncertain response does
    // not prove that the usage reset has been observed.
    if (m_resetBusy) return;
    const auto weekly = chatGptWeekly();
    if (weekly.isEmpty()) return;
    bool validUsage = false;
    const double utilization = weekly.value("utilization").toDouble(&validUsage);
    if (!validUsage || !std::isfinite(utilization) || utilization < 0 || utilization >= 95) return;
    const QString path = resetReceiptPath();
    QJsonObject receipt;
    if (!path.isEmpty() && readReceipt(path, receipt)) {
        if (!receipt.isEmpty()
            && receipt.value("account_fingerprint").toString() == weekly.value("account_fingerprint").toString()
            && QFile::remove(path)) {
            if (m_resetBlockedReceipt == path) m_resetBlockedReceipt.clear();
            m_resetMessage = "Weekly usage has been updated.";
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
    m_resetMessage = "The reset result is unknown. Waiting for weekly usage to drop below 95% before allowing another reset.";
    if (automatic && m_notifications)
        m_notificationCenter.post("providerCard_Codex", "ChatGPT · Automatic reset", m_resetMessage);
    // Leave the durable receipt intact, including when the app quits mid-request.
}

void Controller::consumeChatGptReset() {
    // DO NOT TEST OR INVOKE for validation: this spends a valuable real reset.
    // The explicit Use now button supersedes any pending automatic choice.
    if (!resetAction().value("canConfirm").toBool()) return;
    clearScheduledChatGptReset();
    submitChatGptReset(false);
}

void Controller::submitChatGptReset(bool automatic) {
    // Only explicit immediate or one-shot scheduled consent reaches here.
    // Never retry automatically, follow redirects, or offer another request
    // until usage below 95% has released the durable account latch.
    if (!resetAction().value("canConfirm").toBool()) return;
    const QString receiptPath = resetReceiptPath();
    const auto weekly = chatGptWeekly();
    const QString fingerprint = weekly.value("account_fingerprint").toString();
    QJsonObject receipt;
    if (!readReceipt(receiptPath, receipt) || !receipt.isEmpty()) {
        m_resetBlockedReceipt = receiptPath;
        m_resetMessage = "Check your previous reset in ChatGPT before continuing.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    if (!legacyResetReceiptPaths().isEmpty()) {
        m_resetMessage = "A previous reset request is not bound to a ChatGPT account and cannot be retried safely. Check it in ChatGPT.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    const QString requestWindow = canonicalWindow(weekly);
    receipt = {{"request_id", QUuid::createUuid().toString(QUuid::WithoutBraces)},
        {"completed", false}, {"account_fingerprint", fingerprint},
        {"weekly_resets_at", requestWindow.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(requestWindow)}};
    if (!writeReceipt(receiptPath, receipt)) {
        m_resetMessage = "The reset request could not be saved safely. No reset was requested.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    auto url = Usage::endpoint(backendUrl());
    QString path = url.path();
    path.chop(QStringLiteral("usage").size());
    url.setPath(path + QStringLiteral("providers/codex/reset"));
    QNetworkRequest request(url);
    request.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    request.setRawHeader("Accept", "application/json");
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(95000);
    const ServerConnection transport = m_mode == "local" ? m_server.connection()
        : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), QSslCertificate()};
    if (!transport.token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + transport.token);
    if (m_mode == "local") ServerTransport::secureRequest(request, transport.certificate);
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
    if (m_mode == "local") ServerTransport::requirePinnedPeer(reply, transport.certificate);
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(100000);
    connect(reply, &QNetworkReply::readyRead, this, [reply] { if (reply->bytesAvailable() > 4096) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply, receiptPath, receipt, requestWindow, automatic]() mutable {
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
        const auto error = reply->error(); const auto bytes = reply->readAll();
        reply->deleteLater(); m_resetReply.clear(); m_resetBusy = false;
        const auto outcome = bytes.size() <= 4096 ? QJsonDocument::fromJson(bytes).object().value("outcome").toString() : QString();
        const bool terminal = outcome == "reset" || outcome == "already_redeemed" || outcome == "nothing_to_reset" || outcome == "no_credit";
        if (!redirected && error == QNetworkReply::NoError && status == 200 && terminal) {
            // Retain the submitted window as receipt metadata, never as a
            // reason to release the latch while usage remains at or above 95%.
            if (outcome == "reset") receipt.insert("weekly_resets_at",
                requestWindow.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(requestWindow));
            receipt.insert("completed", true);
            writeReceipt(receiptPath, receipt); // The original receipt keeps the latch if this write fails.
            m_resetBlockedReceipt = receiptPath;
            if (outcome == "reset") m_resetMessage = "One banked reset was used. Refreshing usage…";
            else if (outcome == "already_redeemed") m_resetMessage = "This reset request was already completed. Refreshing usage…";
            else if (outcome == "no_credit") m_resetMessage = "No banked resets are available. Refreshing usage…";
            else m_resetMessage = "There is no usage to reset. Refreshing usage…";
        } else if (status == 404) m_resetMessage = "Update your usage server to use resets here, or open ChatGPT's usage page.";
        else if (status == 409) m_resetMessage = "The server could not safely proceed. Refresh usage or check the previous reset in ChatGPT.";
        else if (status == 401 || status == 403) m_resetMessage = "The reset request was rejected. Check your connection and ChatGPT sign-in.";
        else m_resetMessage = "The reset result is unknown. Waiting for weekly usage to drop below 95% before allowing another reset.";
        if (automatic && m_notifications)
            m_notificationCenter.post("providerCard_Codex", "ChatGPT · Automatic reset", m_resetMessage);
        emit changed();
        // DO NOT test these callbacks as part of the redemption flow. They
        // only GET usage; they must never repeat or confirm a reset request.
        // The server refetches OpenAI after three seconds. These bounded reads
        // allow for propagation/network delay, then normal polling takes over.
        const QString connection = resetConnectionIdentity();
        if (m_startPolling) m_poll.start(m_interval * 1000);
        for (const int delay : {5000, 12000, 25000}) {
            QTimer::singleShot(delay, this, [this, connection] {
                if (chatGptResetAwaitingUsage() && resetConnectionIdentity() == connection) refresh();
            });
        }
    });
}
