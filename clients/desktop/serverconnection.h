#pragma once

#include <QNetworkReply>
#include <QNetworkRequest>
#include <QSslCertificate>
#include <QSslConfiguration>
#include <QSslSocket>
#include <QSslError>
#include <QUrl>

struct ServerConnection {
    QUrl url;
    QByteArray token;
    QSslCertificate certificate;

    bool isPinned() const { return !certificate.isNull(); }
};

namespace ServerTransport {
inline void secureRequest(QNetworkRequest &request, const QSslCertificate &certificate)
{
    if (certificate.isNull()) return;
    QSslConfiguration configuration = QSslConfiguration::defaultConfiguration();
    configuration.setProtocol(QSsl::TlsV1_2OrLater);
    configuration.setPeerVerifyMode(QSslSocket::VerifyPeer);
    configuration.setPeerVerifyDepth(1);
    configuration.setCaCertificates({certificate});
    configuration.setAllowedNextProtocols({QByteArrayLiteral("http/1.1")});
    request.setSslConfiguration(configuration);
}

inline void requirePinnedPeer(QNetworkReply *reply, const QSslCertificate &certificate, bool remote = false)
{
    if (certificate.isNull()) return;
    const QByteArray expected = certificate.toDer();
    QObject::connect(reply, &QNetworkReply::encrypted, reply, [reply, expected] {
        if (reply->sslConfiguration().peerCertificate().toDer() != expected) {
            reply->setProperty("headroomPinMismatch", true); reply->abort();
        }
    });
    if (remote) QObject::connect(reply, &QNetworkReply::sslErrors, reply, [reply, expected](const QList<QSslError> &errors) {
        if (reply->sslConfiguration().peerCertificate().toDer() != expected) {
            reply->setProperty("headroomPinMismatch", true); reply->abort(); return;
        }
        QList<QSslError> allowed;
        for (const auto &error : errors)
            if (error.error() == QSslError::HostNameMismatch && error.certificate().toDer() == expected) allowed.append(error);
        if (!allowed.isEmpty()) reply->ignoreSslErrors(allowed);
    });
}
}
