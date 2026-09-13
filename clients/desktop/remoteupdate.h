#pragma once

#include "sshnetwork.h"
#include <QNetworkReply>
#include <QObject>
#include <QPointer>
#include <QProcess>
#include <QTimer>
#include <functional>

struct RemoteUpdateOptions {
    SshOptions ssh;
    int commandTimeoutMs = 10 * 60 * 1000;
    int verificationTimeoutMs = 90000;
    int pollIntervalMs = 2000;
    std::function<void(const QString &)> diagnostic;
};

// An explicit SSH action invokes the remote installation's existing updater.
// It grants no update authority to HTTP servers and never controls services itself.
class RemoteUpdateService final : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool available READ available NOTIFY changed)
    Q_PROPERTY(bool busy READ busy NOTIFY changed)
    Q_PROPERTY(bool canStart READ canStart NOTIFY changed)
    Q_PROPERTY(QString state READ state NOTIFY changed)
    Q_PROPERTY(QString statusText READ statusText NOTIFY changed)
public:
    explicit RemoteUpdateService(RemoteUpdateOptions options = {}, QObject *parent = nullptr);
    ~RemoteUpdateService() override;
    bool available() const { return !m_address.isEmpty(); }
    bool busy() const { return m_state == "running" || m_state == "verifying"; }
    bool canStart() const { return available() && m_enabled && !busy(); }
    QString state() const { return m_state; }
    QString statusText() const { return m_status; }
    void setBackend(const QString &address);
    void setEnabled(bool enabled);
    Q_INVOKABLE void start();
signals:
    void changed();
    void completed();
private:
    void finishCommand(int exitCode, QProcess::ExitStatus exitStatus);
    void verifyServer();
    void stopWork();
    void fail(const QString &message);
    void log(const QString &message) const;
    RemoteUpdateOptions m_options;
    SshNetworkAccessManager m_network;
    QUrl m_address;
    QPointer<QProcess> m_process;
    QPointer<QNetworkReply> m_reply;
    QTimer m_deadline, m_poll;
    QByteArray m_output;
    qsizetype m_errorBytes = 0;
    QString m_targetVersion;
    QString m_state = "idle", m_status;
    bool m_enabled = false;
};
