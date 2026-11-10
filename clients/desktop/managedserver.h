#pragma once

#include <QNetworkAccessManager>
#include <QPointer>
#include <QProcess>
#include <QSslCertificate>
#include <QTimer>
#include <QUrl>
#include <functional>

#include "serverconnection.h"

struct ManagedServerOptions {
    QUrl localUrl = QUrl(QStringLiteral("http://127.0.0.1:7823/"));
    QString executablePath;
    // Windows Server 2022 runners can take just over four seconds to report
    // ConnectionRefused for an unused numeric loopback port.
    int probeTimeoutMs = 8000;
    int readinessProbeTimeoutMs = 1000;
    int readinessIntervalMs = 100;
    int readinessAttempts = 40;
    int restartLimit = 3;
    int sessionHandshakeTimeoutMs = 8000;
    std::function<void(QNetworkReply *)> probeObserver;
};

class ManagedServer : public QObject {
    Q_OBJECT
public:
    explicit ManagedServer(ManagedServerOptions options = {}, QObject *parent = nullptr);
    ~ManagedServer() override;

    void configure(const QString &mode, const QString &token);
    void ensureAvailable();
    void reportConnectionFailure();
    void stopOwned();
    bool ownsProcess() const { return !m_process.isNull(); }
    bool isAttached() const { return m_state == State::Attached; }
    bool isAvailable() const { return m_state == State::Attached || m_state == State::Started; }
    qint64 ownedProcessId() const { return ownsProcess() ? qint64(m_process->processId()) : 0; }
    QString ownedExecutablePath() const { return ownsProcess() ? binaryPath() : QString(); }
    QString state() const { return stateName(m_state); }
    QString message() const { return m_message; }
    QUrl localUrl() const { return m_options.localUrl; }
    ServerConnection connection() const;

signals:
    void available();
    void unavailable(const QString &message, const QString &kind);
    void stateChanged();
    void connectionChanged();

private:
    enum class State { Remote, Idle, Probing, Starting, Restarting, Attached, Started, Failed };
    enum class ProbePurpose { Initial, Readiness };
    enum class ProbeResult { Compatible, Refused, AuthRejected, Redirected, Malformed, TimedOut, NetworkFailure };

    void cancelAsync();
    void probe(ProbePurpose purpose);
    void handleProbe(ProbePurpose purpose, ProbeResult result);
    void spawn();
    void collectIdentity();
    void finishIdentity();
    void clearSession();
    void failOwnedStartup(const QString &message, const QString &kind);
    void beginReadiness();
    static QString stateName(State state);
    static bool allowed(State from, State to);
    void enter(State next, const QString &message = {});
    void setFailure(const QString &message, const QString &kind);
    void disposeProcess(bool kill);
    QString binaryPath() const;
    bool assignWindowsJob();
    void closeWindowsJob();

    ManagedServerOptions m_options;
    QNetworkAccessManager m_network;
    QPointer<QNetworkReply> m_probe;
    // Only spawned children occupy this pointer: Started requires it, Attached excludes it.
    QPointer<QProcess> m_process;
    QPointer<QProcess> m_retiringProcess;
    QTimer m_readinessTimer;
    QTimer m_restartTimer;
    QTimer m_identityTimer;
    QString m_mode = QStringLiteral("remote");
    QString m_token;
    State m_state = State::Remote;
    QString m_message;
    QByteArray m_identityOutput;
    QByteArray m_sessionNonce;
    QByteArray m_sessionToken;
    QSslCertificate m_sessionCertificate;
    QUrl m_sessionUrl;
    quint64 m_generation = 0;
    int m_readinessAttempt = 0;
    int m_restartCount = 0;
    bool m_ensureAfterRetire = false;
    void *m_job = nullptr;
};
