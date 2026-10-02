#pragma once

#include <QHash>
#include <QNetworkAccessManager>
#include <QPointer>
#include <QProcess>
#include <QTimer>
#include <QTemporaryDir>
#include <QUrl>
#include <QVariantList>
#include <memory>

#include "serverconnection.h"
#include "sshnetwork.h"

struct CredentialServiceOptions {
    QString helperPath;
    int helperTimeoutMs = 5000;
    int requestTimeoutMs = 12000;
    int retryCooldownMs = 5 * 60 * 1000;
    SshOptions sshOptions;
#ifdef Q_OS_WIN
    bool enabled = true;
#else
    bool enabled = false;
#endif
};

class CredentialService : public QObject {
    Q_OBJECT
public:
    explicit CredentialService(CredentialServiceOptions options = {}, QObject *parent = nullptr);
    ~CredentialService() override;

    void configure(const QString &mode, const QString &baseUrl, const QString &token,
                   const QSslCertificate &certificate = QSslCertificate(), bool shareBrowserSignIns = true);
    void consider(const QVariantList &providers);
    QString sharingStatus() const;
    QVariantList checked(const QString &provider) const { return m_checked.value(provider); }
    bool busy() const { return m_process || m_reply || !m_retiringProcesses.isEmpty(); }

signals:
    void providerRecovered(const QVariantMap &provider);
    void event(const QString &message);
    void checkedChanged();

private:
    struct AttemptState {
        bool condition = false;
        qint64 nextDiscovery = 0;
        QByteArray successfulFingerprint;
    };

    void cancel();
    void cancelActiveAttempt();
    void resolvePolicy(const QString &provider);
    void startHelper(const QString &provider, bool localNoProxy);
    void finishHelper(bool success);
    void submit(const QString &provider, QByteArray cookie, const QByteArray &fingerprint, bool localNoProxy, const QString &source);
    void finishRequest(const QString &provider, const QByteArray &fingerprint, QNetworkReply *reply);
    void continueQueue();
    QUrl credentialEndpoint(const QString &provider) const;
    QString helperPath() const;

    SshNetworkAccessManager m_sshNetwork;
    CredentialServiceOptions m_options;
    QNetworkAccessManager m_remoteNetwork;
    QNetworkAccessManager m_localNetwork;
    QPointer<QProcess> m_process;
    QList<QProcess *> m_retiringProcesses;
    QPointer<QNetworkReply> m_reply;
    QTimer m_helperDeadline;
    QString m_mode;
    QString m_baseUrl;
    QString m_token;
    QSslCertificate m_certificate;
    QString m_activeProvider;
    std::shared_ptr<QTemporaryDir> m_snapshotDirectory;
    QByteArray m_helperOutput;
    QStringList m_queue;
    QHash<QString, AttemptState> m_attempts;
    QHash<QString, bool> m_hasAuth;
    QHash<QString, QVariantList> m_checked;
    bool m_shareBrowserSignIns = true;
    quint64 m_operation = 0;
    bool m_activeLocalNoProxy = false;
    bool m_helperFailed = false;
};
