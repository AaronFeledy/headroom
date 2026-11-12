#pragma once

#include <QNetworkAccessManager>
#include <QUrl>

struct SshOptions {
    QString executablePath;
    int timeoutMs = 25000;
};

namespace SshTransport {
bool parseAddress(const QString &address, QUrl *normalized = nullptr, QString *error = nullptr);
// The fixed method/path allowlist and its body rule. Pure decisions, so the
// allowlist can be reviewed and tested without opening a connection.
bool allowsRequest(const QByteArray &method, const QString &path);
bool allowsBody(const QByteArray &method, const QString &path);
QStringList arguments(const QUrl &address);
QStringList updateArguments(const QUrl &address);
}

class SshNetworkAccessManager : public QNetworkAccessManager {
    Q_OBJECT
public:
    explicit SshNetworkAccessManager(SshOptions options = {}, QObject *parent = nullptr);
    void setOptions(SshOptions options);

protected:
    QNetworkReply *createRequest(Operation operation, const QNetworkRequest &request,
                                 QIODevice *outgoingData = nullptr) override;

private:
    SshOptions m_options;
};
