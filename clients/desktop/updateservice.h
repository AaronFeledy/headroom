#pragma once

#include <QObject>
#include <QPointer>
#include <QProcess>
#include <QJsonObject>
#include <QJsonArray>
#include <QTimer>
#include <functional>

struct UpdateServiceOptions {
    QString managerPath;
    QString installRoot;
    QString launcherPath;
    QString packageVersion;
    QString applicationPath;
    int timeoutMs = 150000;
    int cancelGraceMs = 3000;
    bool fixtureIdentity = false;
    bool systemManaged = false;
    // Receives only authored summaries, never raw package-tool output or URLs.
    std::function<void(const QString &)> diagnostic;
};

class UpdateService : public QObject {
    Q_OBJECT
    Q_PROPERTY(QString state READ state NOTIFY changed)
    Q_PROPERTY(QString statusText READ statusText NOTIFY changed)
    Q_PROPERTY(QString latestVersion READ latestVersion NOTIFY changed)
    Q_PROPERTY(bool busy READ busy NOTIFY changed)
    Q_PROPERTY(bool canCancel READ canCancel NOTIFY changed)
    Q_PROPERTY(bool canCheck READ canCheck NOTIFY changed)
    Q_PROPERTY(bool canStage READ canStage NOTIFY changed)
    Q_PROPERTY(bool canRepair READ canRepair NOTIFY changed)
    Q_PROPERTY(bool restartAvailable READ restartAvailable NOTIFY changed)
    Q_PROPERTY(QString updateMethod READ updateMethod NOTIFY changed)
public:
    explicit UpdateService(bool allowPublicTraffic = true, UpdateServiceOptions options = {}, QObject *parent = nullptr);
    ~UpdateService() override;
    QString state() const { return m_state; }
    QString statusText() const { return m_status; }
    QString latestVersion() const { return m_latestVersion; }
    bool busy() const { return m_process || m_pairProcess; }
    bool canCancel() const { return m_process && m_operation != Operation::Apply; }
    bool canCheck() const { return m_allowed && m_official && !busy() && !m_cliReply; }
    bool canStage() const { return m_allowed && m_state == QStringLiteral("available") && !busy(); }
    bool canRepair() const { return m_allowed && m_repairable && !busy() && !m_cliReply; }
    bool restartAvailable() const { return m_allowed && m_state == QStringLiteral("staged"); }
    QString updateMethod() const { return m_method; }
    bool publicUpdatesAllowed() const { return m_allowed && m_official; }
    QString trustedLauncherPath() const { return m_trustedLauncherPath; }
    QJsonObject verifiedStage() const { return m_verifiedStage; }
    Q_INVOKABLE void checkForUpdates();
    Q_INVOKABLE void stageUpdate();
    Q_INVOKABLE void repairInstallation();
    Q_INVOKABLE void restartToApply();
    Q_INVOKABLE void cancel();
    Q_INVOKABLE void openUpdateMethod();
    void startAutomaticCheck();
    void setPublicTrafficAllowed(bool allowed);
    void setOwnedProcessProvider(std::function<QPair<qint64, QString>()> provider) { m_ownedProcessProvider = std::move(provider); }
    void setRelaunchArguments(QStringList arguments) { m_relaunchArguments = std::move(arguments); }
    void requestCLIUpdate(const QJsonObject &request, std::function<void(const QJsonObject &)> reply);
signals:
    void changed();
    void applyPrepared();
private:
    enum class Operation { None, Inspect, Check, Stage, Repair, Apply };
    void inspectInstallation();
    void run(Operation operation, const QString &command, const QStringList &explicitArguments = {});
    void finish(Operation operation, int exitCode, QProcess::ExitStatus exitStatus);
    void fail(const QString &message);
    void log(const QString &message) const;
    bool validateInstalledApplicationIdentity(const QJsonObject &result) const;
    bool validateIdentity(const QJsonObject &result) const;
    bool authorizePreparedApply(const QJsonObject &result) const;
    void handleInspection(const QJsonObject &result);
    void handleUpdateResult(Operation operation, const QJsonObject &result);
    void restoreAllowedState();
    void applyDeferredTrafficState();
    void finishCLIRequest(const QString &status, bool ok);
    void startPairedUpdate();
    QString guidePath() const;
    UpdateServiceOptions m_options;
    QPointer<QProcess> m_process;
    QPointer<QProcess> m_pairProcess;
    bool m_cliAvailable = false;
    QString m_cliEntryPath;
    QString m_trustedLauncherPath;
    QTimer m_timeout;
    QByteArray m_output;
    QByteArray m_errorOutput;
    QJsonObject m_verifiedStage;
    bool m_stageIsRepair = false;
    QString m_state = QStringLiteral("unavailable");
    QString m_status;
    QString m_latestVersion;
    QString m_platform;
    QString m_architecture;
    QString m_packageKind;
    QString m_method = QStringLiteral("source");
    QString m_diagnosticCommand;
    QString m_prePauseState;
    QString m_prePauseStatus;
    bool m_allowed = true;
    bool m_sessionAllowed = true;
    bool m_official = false;
    bool m_repairable = false;
    bool m_autoPending = false;
    bool m_autoStage = false;
    bool m_cancelRequested = false;
    bool m_timedOut = false;
    bool m_autoStarted = false;
    bool m_resumeAfterCancel = false;
    bool m_suppressAutomaticCheck = false;
    bool m_hasDeferredAllowed = false;
    bool m_deferredAllowed = true;
    std::function<QPair<qint64, QString>()> m_ownedProcessProvider;
    QStringList m_relaunchArguments;
    QJsonArray m_cliParticipants;
    QString m_cliRequestNonce;
    std::function<void(const QJsonObject &)> m_cliReply;
    Operation m_operation = Operation::None;
};
