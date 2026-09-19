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

class Controller : public QObject {
    Q_OBJECT
    Q_PROPERTY(Notifications *notifications READ notifications CONSTANT)
    Q_PROPERTY(QVariantList providers READ providers NOTIFY providersChanged)
    Q_PROPERTY(QVariantMap state READ state NOTIFY changed)
    Q_PROPERTY(QVariantMap settings READ settings NOTIFY settingsChanged)
    Q_PROPERTY(QVariantList diagnostics READ diagnostics NOTIFY diagnosticsChanged)
    Q_PROPERTY(QVariantMap resetAction READ resetAction NOTIFY changed)
public:
    explicit Controller(const QString &configPath = {}, QObject *parent = nullptr,
                        bool allowAutomaticMigration = true, ManagedServerOptions serverOptions = {},
                        CredentialServiceOptions credentialOptions = {}, SshOptions sshOptions = {}, bool startPolling = true);
    ~Controller() override;
    Notifications *notifications() { return &m_notificationCenter; }
    QVariantList providers() const;
    QVariantMap state() const;
    QVariantMap settings() const;
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
    Q_INVOKABLE virtual void refresh();
    QVariantMap resetAction() const;
    // DO NOT test this button, endpoint, or any code that could trigger a reset.
    // A reset is valuable and irreversible; the skip-only tests are intentional.
    Q_INVOKABLE bool prepareChatGptReset();
    Q_INVOKABLE void cancelChatGptResetConfirmation();
    Q_INVOKABLE void consumeChatGptReset();
    Q_INVOKABLE QString warningColor(int severity) const;
    Q_INVOKABLE QString displayName(const QString &provider) const;
    Q_INVOKABLE QString saveSettings(QString mode, QString url, QString token, int interval, bool notifications, QString primary, bool forgetToken, QString sshUrl = {});
    Q_INVOKABLE QVariantMap concern(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QVariantList notches(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QVariantMap pacing(const QString &provider, const QVariantMap &bucket) const;
    Q_INVOKABLE QString countdown(const QString &timestamp) const;
    Q_INVOKABLE void setPrimary(const QString &name);
    Q_INVOKABLE void copyText(const QString &text);
    QString primary() const;
    Q_INVOKABLE void moveProvider(const QString &source, const QString &target, bool after);
signals:
    void changed();
    void diagnosticsChanged();
    void providersChanged();
    void settingsChanged();
protected:
    void acceptSnapshot(const QVariantList &providers);
private:
    void updateMeterStates();
    void fail(const QString &message, const QString &kind = "network");
    void log(const QString &category, const QString &message);
    void resetRetry();
    void cancel();
    void requestUsage();
    void syncConnection();
    QVariantMap chatGptWeekly() const;
    bool chatGptResetEligible() const;
    bool chatGptResetAwaitingUsage() const;
    QString resetConnectionIdentity() const;
    QString resetReceiptPath() const;
    QStringList legacyResetReceiptPaths() const;
    void observeResetUsage();
    void cancelResetRequest();
    QString writeSettings(const QString &mode, const QString &url, const QString &token, const QString &sshUrl, int interval, bool notifications, const QString &primary);
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
    using MeterKey = QPair<QString, QString>;
    QHash<MeterKey, Usage::WarningState> m_warningStates;
    QHash<MeterKey, QVariantMap> m_concerns;
    QNetworkAccessManager m_network;
    QNetworkAccessManager m_localNetwork;
    SshNetworkAccessManager m_sshNetwork;
    ManagedServer m_server;
    CredentialService m_credentials;
    QPointer<QNetworkReply> m_reply;
    QPointer<QNetworkReply> m_resetReply;
    QString m_resetConfirmation, m_resetMessage, m_resetBlockedReceipt;
    bool m_resetBusy = false;
    QTimer m_poll, m_clock;
};
