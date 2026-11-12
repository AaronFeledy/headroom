#include "controller.h"
#include "usage.h"
#include "resetpolicy.h"
#include <QCryptographicHash>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
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

// Feed one provider row's meters and validated kinds into policy facts. The
// card uses this for any row, including read-only multi-account rows.
void appendResetBuckets(const QVariantList &buckets, ResetPolicy::Facts &facts) {
    for (const auto &entry : buckets) {
        const auto meter = entry.toMap();
        ResetPolicy::Bucket bucket;
        bucket.id = meter.value("id").toString();
        bucket.label = meter.value("label").toString();
        bool valid = false;
        const double used = meter.value("utilization").toDouble(&valid);
        if (valid && std::isfinite(used)) bucket.utilization = used;
        if (!bucket.id.isEmpty()) facts.buckets.append(bucket);
    }
}

void appendResetKinds(const QVariant &kindsValue, ResetPolicy::Facts &facts) {
    if (kindsValue.metaType().id() != QMetaType::QVariantList) return;
    facts.kindsKnown = true;
    for (const auto &entry : kindsValue.toList()) {
        const auto kind = entry.toMap();
        ResetPolicy::Kind parsed;
        parsed.id = kind.value("id").toString();
        parsed.label = kind.value("label").toString();
        parsed.availableCount = kind.value("available_count").toLongLong();
        parsed.usableNow = kind.value("usable_now").toBool();
        parsed.refills = kind.value("refills").toStringList();
        parsed.givesBackWeekly = kind.value("gives_back_weekly").toBool();
        parsed.expiresAt = kind.value("expires_at").toString();
        // A malformed or absent grant already normalized to null at the API
        // boundary, so only a real map marks a spendable grant.
        if (kind.value("next").metaType().id() == QMetaType::QVariantMap) {
            const auto next = kind.value("next").toMap();
            parsed.hasNext = true;
            parsed.nextLabel = next.value("label").toString();
            parsed.nextExpiresAt = next.value("expires_at").toString();
        }
        facts.kinds.append(parsed);
    }
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
    return m_status == ConnectionStatus::Ready && m_request == Request::None && m_lastGood > 0 && now - m_lastGood <= maximumAge
        && (provider != "Claude" || fetchedAt.isValid());
}

QVariantMap Controller::resetAutomation(const QString &provider) const {
    if (provider != "Codex" && provider != "Claude") return {};
    for (const auto &value : m_providers) {
        const auto row = value.toMap();
        if (row.value("provider_name").toString() != provider) continue;
        const auto state = row.value("reset_automation");
        return state.isValid() && !state.isNull() ? state.toMap() : QVariantMap{};
    }
    return {};
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
            if (facts.readingAvailable) appendResetBuckets(current.value("buckets").toList(), facts);
            break;
        }
    }
    const auto automation = resetAutomation(provider);
    facts.automationSupported = !automation.isEmpty();
    if (facts.automationSupported) {
        facts.automationArmed = automation.value("armed").toBool();
        facts.automationKinds = automation.value("kinds").toStringList();
        facts.automationWindowEndsAt = automation.value("window_ends_at").toString();
    }
    facts.fresh = resetReadingFresh(provider);
    if (reading.contains("available_count")) facts.availableCount = reading.value("available_count").toLongLong();
    bool validUsage = false;
    const double utilization = reading.value("utilization").toDouble(&validUsage);
    if (facts.readingAvailable && validUsage) facts.utilization = utilization;
    if (reading.value("usable_now").metaType().id() == QMetaType::Bool) facts.usableNow = reading.value("usable_now").toBool();
    // The additive kind breakdown is already validated by the usage parser.
    // Only a real array counts as a known list; null or absent stays legacy.
    if (provider == "Claude") appendResetKinds(reading.value("kinds"), facts);
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
    // The dialog choice is session-only. With several kinds an empty choice
    // means "Any matching reset"; a sole listed kind is preselected. A choice
    // that has left the reading keeps its own missing state: it is never
    // replaced by another kind and never widened to "any".
    const QString selection = m_resetKind;
    if (selection.isEmpty() && facts.automationArmed && facts.automationKinds.size() == 1) {
        if (const auto chosen = ResetPolicy::findKind(facts, facts.automationKinds.first()))
            facts.selectedKind = chosen->id;
    } else if (selection.isEmpty()) {
        if (facts.kinds.size() == 1) facts.selectedKind = facts.kinds.first().id;
    } else if (const auto chosen = ResetPolicy::findKind(facts, selection)) {
        facts.selectedKind = chosen->id;
    } else {
        facts.selectedKindMissing = facts.kindsKnown;
    }
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
    using ResetPolicy::Phase;
    const bool armed = capabilities.phase == Phase::Armed || capabilities.phase == Phase::Paused;
    QVariantList kinds;
    for (const auto &kind : facts.kinds) {
        kinds.append(QVariantMap{{"id", kind.id}, {"label", kind.label},
            {"availableCount", QVariant::fromValue(kind.availableCount)},
            {"usableNow", kind.usableNow}, {"refills", kind.refills},
            {"givesBackWeekly", kind.givesBackWeekly}, {"expiresAt", kind.expiresAt},
            {"countText", ResetPolicy::kindCountText(kind)}, {"title", ResetPolicy::kindTitle(kind)},
            {"refillText", ResetPolicy::kindRefillText(facts, kind)},
            {"triggerText", ResetPolicy::kindApplicabilityText(facts, kind)},
            {"returnText", ResetPolicy::kindReturnText(facts, kind)},
            {"nextText", ResetPolicy::kindNextText(kind, Usage::resetTimeLabel(kind.nextExpiresAt))}});
    }
    QVariantList allowed;
    for (const auto &id : facts.automationKinds) {
        const auto *kind = ResetPolicy::findKind(facts, id);
        allowed.append(kind ? kind->label : id);
    }
    const auto automation = resetAutomation(m_resetProvider);
    const auto lastEvent = automation.value("last_event").toMap();
    const auto selected = ResetPolicy::findKind(facts, facts.selectedKind);
    return {{"provider", m_resetProvider}, {"busy", m_resetBusy}, {"message", m_resetMessage}, {"automatic", armed},
        {"availableCount", facts.availableCount ? QVariant::fromValue(*facts.availableCount) : QVariant()},
        {"awaitingUsage", facts.awaitingUsage},
        {"enabled", armed || (m_resetConfirmation.isEmpty() && capabilities.canUseNow)},
        {"phase", ResetPolicy::phaseName(capabilities.phase)},
        {"canConfirm", capabilities.canConfirm}, {"canSchedule", capabilities.canSchedule},
        {"canCancel", capabilities.canCancelArm},
        {"useNowReason", ResetPolicy::reasonText(capabilities.useNowReason, facts.provider)},
        {"automaticReason", ResetPolicy::reasonText(capabilities.armReason, facts.provider)},
        {"kinds", kinds}, {"kindsKnown", facts.kindsKnown},
        {"selectedKind", selected ? selected->id : QString()},
        {"selectedKindMissing", facts.selectedKindMissing},
        {"selectedKindTitle", selected ? ResetPolicy::kindTitle(*selected) : QString()},
        {"selectedKindCount", selected ? ResetPolicy::kindCountText(*selected) : QString()},
        {"selectedKindTrigger", selected ? ResetPolicy::kindApplicabilityText(facts, *selected) : QString()},
        {"automaticEffect", ResetPolicy::automaticEffectText(facts)},
        {"automaticKinds", allowed}, {"lastEvent", lastEvent},
        {"requested", lastEvent.value("event").toString() == QStringLiteral("requested")},
        {"useNowGrant", selected ? ResetPolicy::kindUseNowText(*selected, Usage::resetTimeLabel(selected->nextExpiresAt)) : QString()},
        {"windowEndsAt", armed && !facts.automationWindowEndsAt.isEmpty()
            ? facts.automationWindowEndsAt : canonicalWindow(resetReading(m_resetProvider))}};
}

QVariantMap Controller::resetKindContent(const QString &name) const {
    // The card serves any provider row by name, including read-only
    // multi-account rows that have no reset action. Wording still comes from
    // the policy; only this row's own meters and kinds feed it.
    ResetPolicy::Facts facts;
    for (const auto &value : m_providers) {
        const auto row = value.toMap();
        if (row.value("provider_name").toString() != name || !row.value("is_success").toBool()) continue;
        appendResetBuckets(row.value("buckets").toList(), facts);
        appendResetKinds(row.value("rate_limit_reset_credits").toMap().value("kinds"), facts);
        break;
    }
    QStringList counts, details;
    const QString separator = QStringLiteral(" %1 ").arg(QChar(0x00B7));
    for (const auto &kind : facts.kinds) {
        counts.append(ResetPolicy::kindCountText(kind));
        details.append(ResetPolicy::kindDetailText(facts, kind, Usage::resetTimeLabel(kind.expiresAt)));
    }
    return {{"countText", counts.join(separator)}, {"lines", counts.join(QChar('\n'))},
        {"details", details.join(QChar('\n'))}};
}

bool Controller::prepareReset(const QString &provider) {
    // Opens one provider's options. The policy alone grants permission to
    // use or arm; this binds the options to the current account/connection
    // identity and explains a pending receipt. It never sends a request.
    if (provider != "Codex" && provider != "Claude") return false;
    if (!ResetPolicy::evaluate(resetFacts(provider)).canOpen) return false;
    m_resetProvider = provider;
    // A closed dialog starts with no choice when several kinds are listed,
    // which selects "Any matching reset"; a sole kind is preselected.
    m_resetKind.clear();
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
    m_resetConfirmation.clear();
    m_resetKind.clear();
    emit changed();
}

bool Controller::selectResetKind(const QString &kind) {
    // Recording a choice is not permission and never spends: the current
    // reading and the policy still decide whether it may be used. An empty
    // id is the "Any matching reset" row, so it always clears a specific
    // choice and returns to the any-matching state.
    if (kind.isEmpty()) {
        m_resetKind.clear();
        emit changed();
        return true;
    }
    const auto facts = resetFacts("Claude");
    if (!ResetPolicy::findKind(facts, kind)) return false;
    m_resetKind = kind;
    emit changed();
    return true;
}

ServerConnection Controller::resetTransport() const {
    // One transport definition for arm, cancel, and redeem requests so their
    // bearer and certificate handling can never drift apart.
    return m_mode == "local" ? m_server.connection()
        : ServerConnection{QUrl(backendUrl()), backendToken().toUtf8(), backendCertificate()};
}

QNetworkRequest Controller::resetRouteRequest(const QString &provider, const QString &route) const {
    auto url = Usage::endpoint(backendUrl());
    QString path = url.path();
    path.chop(QStringLiteral("usage").size());
    url.setPath(path + QStringLiteral("providers/") + provider.toLower() + QStringLiteral("/") + route);
    QNetworkRequest request(url);
    request.setHeader(QNetworkRequest::ContentTypeHeader, "application/json");
    request.setRawHeader("Accept", "application/json");
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(95000);
    const ServerConnection transport = resetTransport();
    if (!transport.token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + transport.token);
    ServerTransport::secureRequest(request, transport.certificate, m_mode == QStringLiteral("remote"));
    return request;
}

void Controller::cancelAutomationRequest() {
    if (!m_automationReply) return;
    disconnect(m_automationReply, nullptr, this, nullptr);
    m_automationReply->abort();
    m_automationReply->deleteLater();
    m_automationReply.clear();
}

void Controller::finishAutomationRequest(QNetworkReply *reply, bool arming) {
    const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
    const bool redirected = reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid();
    const auto error = reply->error();
    const auto bytes = reply->readAll();
    reply->deleteLater();
    if (m_automationReply == reply) m_automationReply.clear();
    if (!redirected && error == QNetworkReply::NoError && status == 200) {
        m_resetMessage.clear();
        // One ordinary cache read makes the server's new state visible.
        if (m_startPolling) m_poll.start(0);
        emit changed();
        return;
    }
    const auto answer = bytes.size() <= 4096 ? QJsonDocument::fromJson(bytes).object() : QJsonObject{};
    const QString serverError = answer.value("error").toString();
    if (status == 404 || status == 405) m_resetMessage = "Update the server to use automatic resets.";
    else if (!serverError.isEmpty()) m_resetMessage = serverError;
    else m_resetMessage = arming ? "The automatic reset could not be set. Check the connection."
        : "The automatic reset could not be canceled. Check the connection.";
    emit changed();
}

bool Controller::scheduleReset() {
    // This button asks the server to store ONE future authorization. It never
    // spends and never retries. DO NOT invoke it during testing.
    const auto facts = resetFacts(m_resetProvider);
    if (!ResetPolicy::evaluate(facts).canSchedule) return false;
    if (m_automationReply) return false;
    const auto weekly = resetReading(m_resetProvider);
    QJsonObject payload{{"confirmed", true},
        {"account_fingerprint", weekly.value("account_fingerprint").toString()},
        {"window_ends_at", canonicalWindow(weekly)}};
    if (m_resetProvider == "Claude") {
        QJsonArray kinds;
        for (const auto &id : ResetPolicy::automaticKinds(facts)) kinds.append(id);
        payload.insert("kinds", kinds);
    }
    const auto body = QJsonDocument(payload).toJson(QJsonDocument::Compact);
    auto request = resetRouteRequest(m_resetProvider, QStringLiteral("reset/automatic"));
    request.setHeader(QNetworkRequest::ContentLengthHeader, qint64(body.size()));
    request.setAttribute(QNetworkRequest::Http2AllowedAttribute, false);
    const ServerConnection transport = resetTransport();
    QNetworkAccessManager *network = m_mode == "local" ? &m_localNetwork
        : m_mode == "ssh" ? static_cast<QNetworkAccessManager *>(&m_sshNetwork) : &m_network;
    // Sending the arm request keeps the open dialog bound: a failed or
    // aborted request must not lock it until it is closed and reopened.
    m_resetMessage = "Setting the automatic reset…";
    emit changed();
    auto reply = network->put(request, body);
    m_automationReply = reply;
    ServerTransport::requirePinnedPeer(reply, transport.certificate, m_mode == QStringLiteral("remote"));
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(100000);
    connect(reply, &QNetworkReply::readyRead, this, [reply] { if (reply->bytesAvailable() > 4096) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply] { finishAutomationRequest(reply, true); });
    return true;
}

void Controller::cancelScheduledReset() {
    // Server-held state is canceled explicitly; a failed cancel never spends.
    const auto facts = resetFacts(m_resetProvider);
    if (!ResetPolicy::evaluate(facts).canCancelArm) return;
    if (m_automationReply) return;
    auto request = resetRouteRequest(m_resetProvider, QStringLiteral("reset/automatic"));
    const ServerConnection transport = resetTransport();
    QNetworkAccessManager *network = m_mode == "local" ? &m_localNetwork
        : m_mode == "ssh" ? static_cast<QNetworkAccessManager *>(&m_sshNetwork) : &m_network;
    m_resetMessage = "Canceling the automatic reset…";
    emit changed();
    auto reply = network->deleteResource(request);
    m_automationReply = reply;
    ServerTransport::requirePinnedPeer(reply, transport.certificate, m_mode == QStringLiteral("remote"));
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(100000);
    connect(reply, &QNetworkReply::finished, this, [this, reply] { finishAutomationRequest(reply, false); });
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
    disconnect(m_resetReply, nullptr, this, nullptr);
    m_resetReply->abort(); m_resetReply->deleteLater(); m_resetReply.clear();
    m_resetBusy = false;
    m_resetMessage = ResetPolicy::awaitingUsageText(providerKind(m_resetProvider), true);
    // Leave the durable receipt intact, including when the app quits mid-request.
}

void Controller::consumeReset() {
    // DO NOT TEST OR INVOKE for validation: this spends a valuable real reset.
    // A confirmed manual use also cancels the server's automatic authorization.
    if (!resetAction().value("canConfirm").toBool()) return;
    submitReset();
}

void Controller::submitReset() {
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
    // The redeem body carries a kind only when this Claude reading lists kinds
    // and one is chosen. Without kinds, the legacy three-key body is unchanged.
    QString kind;
    if (m_resetProvider == "Claude") {
        const auto facts = resetFacts("Claude");
        if (facts.kindsKnown)
            if (const auto *chosen = ResetPolicy::findKind(facts, facts.selectedKind)) kind = chosen->id;
    }
    receipt = {{"request_id", QUuid::createUuid().toString(QUuid::WithoutBraces)},
        {"completed", false}, {"account_fingerprint", fingerprint}, {"provider", m_resetProvider},
        {"available_count", weekly.value("available_count").toLongLong()},
        {"submitted_at", QDateTime::currentDateTimeUtc().toString(Qt::ISODateWithMs)},
        {"reading_fetched_at", weekly.value("fetched_at").toString()},
        {"weekly_resets_at", requestWindow.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(requestWindow)}};
    // Optional receipt metadata; older receipts without it stay readable.
    if (!kind.isEmpty()) receipt.insert("kind", kind);
    if (!writeReceipt(receiptPath, receipt)) {
        m_resetMessage = "The reset request could not be saved safely. No reset was requested.";
        m_resetConfirmation.clear(); emit changed(); return;
    }
    auto request = resetRouteRequest(m_resetProvider, QStringLiteral("reset"));
    const ServerConnection transport = resetTransport();
    QJsonObject payload{{"request_id", receipt.value("request_id")},
        {"confirmed", true}, {"account_fingerprint", fingerprint}};
    if (!kind.isEmpty()) payload.insert("kind", kind);
    const auto body = QJsonDocument(payload).toJson(QJsonDocument::Compact);
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
    upload->setParent(reply);
    ServerTransport::requirePinnedPeer(reply, transport.certificate, m_mode == QStringLiteral("remote"));
    auto deadline = new QTimer(reply); deadline->setSingleShot(true);
    connect(deadline, &QTimer::timeout, reply, &QNetworkReply::abort); deadline->start(100000);
    connect(reply, &QNetworkReply::readyRead, this, [reply] { if (reply->bytesAvailable() > 4096) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply, receiptPath, receipt, requestWindow]() mutable {
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
