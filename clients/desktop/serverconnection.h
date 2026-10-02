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
inline void secureRequest(QNetworkRequest &request, const QSslCertificate &certificate, bool remote = false)
{
    if (certificate.isNull()) return;
    QSslConfiguration configuration = QSslConfiguration::defaultConfiguration();
    configuration.setProtocol(QSsl::TlsV1_2OrLater);
    configuration.setPeerVerifyMode(QSslSocket::VerifyPeer);
    configuration.setPeerVerifyDepth(1);
    configuration.setCaCertificates({certificate});
    configuration.setAllowedNextProtocols({QByteArrayLiteral("http/1.1")});
    request.setSslConfiguration(configuration);
    if (remote) {
        // A token proof authenticates this exact certificate independently of DNS.
        // Verify a name it actually contains so native TLS backends can validate
        // its trust policy without ignoring hostname or certificate errors.
        const auto names = certificate.subjectAlternativeNames();
        const QString host = request.url().host();
        QString name;
        for (const QString &dns : names.values(QSsl::DnsEntry)) {
            const QString suffix = dns.mid(1);
            const bool wildcardMatch = dns.startsWith("*.") && host.endsWith(suffix, Qt::CaseInsensitive)
                && !host.left(host.size() - suffix.size()).contains('.');
            if (dns.compare(host, Qt::CaseInsensitive) == 0 || wildcardMatch) { name = host; break; }
        }
        if (name.isEmpty() && names.values(QSsl::IpAddressEntry).contains(host)) name = host;
        if (name.isEmpty()) name = names.value(QSsl::DnsEntry);
        if (name.isEmpty()) name = names.value(QSsl::IpAddressEntry);
        if (name.isEmpty()) name = certificate.subjectInfo(QSslCertificate::CommonName).value(0);
        if (name.startsWith("*.")) name = QStringLiteral("headroom") + name.mid(1);
        if (!name.isEmpty()) request.setPeerVerifyName(name);
    }
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
    if (remote) QObject::connect(reply, &QNetworkReply::sslErrors, reply, [reply, expected](const QList<QSslError> &) {
        if (reply->sslConfiguration().peerCertificate().toDer() != expected) {
            reply->setProperty("headroomPinMismatch", true); reply->abort(); return;
        }
    });
}
}
