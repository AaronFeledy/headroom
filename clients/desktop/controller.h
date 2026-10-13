#pragma once
#include <QObject>
#include <QHash>
#include "warning.h"
#include "notifications.h"
#include <QNetworkAccessManager>
#include <QNetworkReply>
#include <QPointer>
#include <QTimer>
#include <QVariantList>
#include <QSet>
#include <QSslCertificate>
#include "settings.h"
#include "managedserver.h"
#include "credentialservice.h"
#include "sshnetwork.h"
#include "tlsproof.h"

class Controller : public QObject {
    Q_OBJECT
    Q_PROPERTY(Notifications *notifications READ notifications CONSTANT)
    Q_PROPERTY(QVariantList providers READ providers NOTIFY providersChanged)
    // Dashboard model: raw providers, except that a transiently failed provider
    // may carry its last successful reading as clearly labelled stale meters.
    Q_PROPERTY(QVariantList displayProviders READ displayProviders NOTIFY providersChanged)
    Q_PROPERTY(QVariantMap refreshStatus READ refreshStatus NOTIFY refreshStatusChanged)
    Q_PROPERTY(QVariantMap state READ state NOTIFY changed)
    Q_PROPERTY(QVariantMap settings READ settings NOTIFY settingsChanged)
    Q_PROPERTY(QVariantList diagnostics READ diagnostics NOTIFY diagnosticsChanged)
    Q_PROPERTY(QVariantMap resetAction READ resetAction NOTIFY changed)
    Q_PROPERTY(QVariantMap browserChecked READ browserChecked NOTIFY providersChanged)
public:
    explicit Controller(const QString &configPath = {}, QObject *parent = nullptr,
                        bool allowAutomaticMigration = true, ManagedServerOptions serverOptions = {},
                        CredentialServiceOptions credentialOptions = {}, SshOptions sshOptions = {}, bool startPolling = true);
    ~Controller() override;
    Notifications *notifications() { return &m_notificationCenter; }
    // Raw server model for IPC/CLI, notifications, warning transitions,
    // credentials, and reset eligibility. Never carries stale readings.
    QVariantList providers() const;
    QVariantList displayProviders() const;
    QVariantMap refreshStatus() const;
    QVariantMap state() const;
    QVariantMap settings() const;
    QVariantMap browserChecked() const;
    // True while a saved http:// remote should first try token-verified HTTPS.
    // Other authenticated requests wait so the bearer is not sent in plain text.
    bool remoteVerificationPending() const;
    QVariantList diagnostics() const { return m_diagnostics; }
    Q_INVOKABLE void clearDiagnostics();
    Q_INVOKABLE QString diagnosticText() const;
    // C++ integration only; UpdateService supplies safe, authored summaries.
    void logUpdate(const QString &message) { log(QStringLiteral("update"), message); }
    // C++ integration only: the bearer token is never a QML property.
    QString backendUrl() const;
    QString backendToken() const;
    QSslCertificate backendCertificate() const;
    bool startupPreference() const { return m_settingsService.value().startup; }
    bool startupMigrationPending() const { return m_settingsService.value().startupMigrationPending; }
    QString settingsPath() const { return m_settingsService.path(); }
    QString saveStartupPreference(bool enabled);
    QString completeStartupMigration();
    QSize windowSize() const { return m_settingsService.value().windowSize; }
    QString saveWindowSize(const QSize &size) { return m_settingsService.saveWindowSize(size); }
    // C++ integration seam for update preparation; attached/remote servers are untouched.
    void stopOwnedServer() { m_server.stopOwned(); }
    qint64 ownedServerProcessId() const { return m_server.ownedProcessId(); }
    QString ownedServerExecutablePath() const { return m_server.ownedExecutablePath(); }
    // Internal cache read of GET /api/v1/usage: startup, reconnects, settings,
    // post-update checks, and recovery polling. Never forces a provider fetch.
    Q_INVOKABLE virtual void refresh();
    // Explicit user action: POST /api/v1/usage/refresh over the selected
    // transport, then bounded cache reads while the server refetches.
    Q_INVOKABLE virtual void requestRefresh();
    Q_INVOKABLE QString readingAgeLabel(qint64 observedAt) const;
    QVariantMap resetAction() const;
    // DO NOT test this button, endpoint, or any code that could trigger a reset.
    // A reset is valuable and irreversible; the skip-only tests are intentional.
    Q_INVOKABLE bool prepareChatGptReset();
    Q_INVOKABLE void cancelChatGptResetConfirmation();
    Q_INVOKABLE void consumeChatGptReset();
    Q_INVOKABLE bool scheduleChatGptReset();
    Q_INVOKABLE void cancelScheduledChatGptReset();
    Q_INVOKABLE QString warningColor(int severity) const;
    Q_INVOKABLE QString displayName(const QString &provider) const;
    Q_INVOKABLE QVariantMap loginCopy(const QVariantMap &provider) const;
    Q_INVOKABLE QString saveSettings(QString mode, QString url, QString token, int interval, bool notifications, QString primary, bool forgetToken, QString sshUrl = {}, bool shareBrowserSignIns = true);
    Q_INVOKABLE QVariantMap concern(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QVariantList notches(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QVariantMap pacing(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QString countdown(const QString &timestamp) const;
    Q_INVOKABLE QString resetTimeLabel(const QString &timestamp) const;
    Q_INVOKABLE void setPrimary(const QString &name);
    Q_INVOKABLE void copyText(const QString &text);
    QString primary() const;
    Q_INVOKABLE void moveProvider(const QString &source, const QString &target, bool after);
signals:
    void changed();
    void diagnosticsChanged();
    void providersChanged();
    void settingsChanged();
    void refreshStatusChanged();
protected:
    void acceptSnapshot(const QVariantList &providers);
private:
    struct RetainedReading {
        QVariantList buckets;
        QString fetchedAt, epoch, source;
        qint64 observedAt = 0;      // desktop time this reading was first seen
        quint64 snapshotSerial = 0; // last accepted snapshot in which the provider succeeded
    };
    void updateMeterStates();
    void fail(const QString &message, const QString &kind = "network");
    void log(const QString &category, const QString &message);
    void resetRetry();
    void cancel();
    void requestUsage();
    void refreshUsage(bool userRequested);
    void requestProviderRefresh();
    QNetworkRequest backendRequest(const QUrl &url, const ServerConnection &transport) const;
    QNetworkAccessManager *transportNetwork();
    void guardReply(QNetworkReply *reply, const ServerConnection &transport, int deadlineMs, qint64 maximumBytes);
    bool rejectRedirectOrToken(int status, bool redirected);
    void reportNetworkFailure(bool certificateFailure);
    void acceptRefresh(const QByteArray &body);
    void limitRefresh(const QByteArray &body);
    void noteRefresh(const QString &kind, const QString &summary);
    void runRecoveryStep();
    void endRefreshWindow();
    bool refreshObserved(const QVariantList &providers) const;
    void syncRefreshClock();
    void invalidateConnection();
    void markBackendUnavailable(bool outage, const QString &message = {});
    void retainReadings(const QVariantList &providers, bool removeMissing);
    bool transientRecoveryActive() const;
    void syncConnection();
    void verifyRemote(bool upgrade);
    QVariantMap chatGptWeekly() const;
    bool chatGptResetEligible() const;
    bool chatGptResetAwaitingUsage() const;
    QString resetConnectionIdentity() const;
    QString resetReceiptPath() const;
    QStringList legacyResetReceiptPaths() const;
    void observeResetUsage();
    void observeScheduledChatGptReset();
    void expireScheduledChatGptReset();
    void clearScheduledChatGptReset();
    void submitChatGptReset(bool automatic);
    void cancelResetRequest();
    QString writeSettings(const QString &mode, const QString &url, const QString &token, const QString &sshUrl, int interval, bool notifications, const QString &primary, bool shareBrowserSignIns);
    Notifications m_notificationCenter;
    QStringList m_order;
    SettingsService m_settingsService;
    QString m_mode = "remote", m_url, m_token, m_sshUrl, m_primary = "Claude", m_message, m_status = "setup";
    int m_interval = 60, m_retryAttempt = 0;
    QString m_errorKind;
    QVariantList m_diagnostics;
    bool m_notifications = true, m_loading = false;
    bool m_startPolling = true;
    bool m_waitingForUsageRetry = false;
    qint64 m_lastGood = 0;
    QVariantList m_providers;
    // Session-only presentation cache: successful buckets only, never action metadata.
    QHash<QString, RetainedReading> m_retained;
    QHash<QString, qint64> m_transientSince;
    QSet<QString> m_maskedProviders; // raw entries masked by a backend failure, not by the server
    quint64 m_snapshotSerial = 0, m_connectionGeneration = 0;
    bool m_backendOutage = false;
    // Explicit refresh bookkeeping. Timers are bounded and never repeat the POST.
    QTimer m_recovery, m_refreshClock;
    QHash<QString, QString> m_refreshBaseline;
    QString m_refreshKind, m_refreshSummary;
    qint64 m_refreshAcceptedAt = 0, m_refreshAllowedAt = 0, m_refreshNoticeUntil = 0;
    int m_recoveryStep = 0;
    using MeterKey = QPair<QString, QString>;
    QHash<MeterKey, Usage::WarningState> m_warningStates;
    QHash<MeterKey, QVariantMap> m_concerns;
    QNetworkAccessManager m_network;
    QNetworkAccessManager m_localNetwork;
    SshNetworkAccessManager m_sshNetwork;
    ManagedServer m_server;
    CredentialService m_credentials;
    TlsProofService m_tlsProof;
    qint64 m_nextUpgradeAttempt = 0;
    bool m_proving = false;
    bool m_proofAttempted = false;
    QPointer<QNetworkReply> m_reply;
    QPointer<QNetworkReply> m_resetReply;
    QString m_resetConfirmation, m_resetMessage, m_resetBlockedReceipt;
    bool m_resetBusy = false;
    // One-shot authorization is session-only and bound to this account,
    // transport, and weekly window. Never restore it on an app restart.
    QString m_autoResetConnection, m_autoResetWindow;
    QTimer m_poll, m_clock;
};
