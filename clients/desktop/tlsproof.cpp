#include "tlsproof.h"
#include "usage.h"
#include <QCryptographicHash>
#include <QJsonDocument>
#include <QJsonObject>
#include <QMessageAuthenticationCode>
#include <QRandomGenerator>
#include <QRegularExpression>
#include <QSslConfiguration>
#include <QSslSocket>
#include <QTimer>
#include <QUrlQuery>

namespace {
bool hex64(const QByteArray &value) {
    if (value.size() != 64) return false;
    for (char c : value) if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return false;
    return true;
}
bool constantEqual(const QByteArray &left, const QByteArray &right) {
    if (left.size() != right.size()) return false;
    unsigned char difference = 0;
    for (qsizetype i = 0; i < left.size(); ++i)
        difference |= static_cast<unsigned char>(left[i]) ^ static_cast<unsigned char>(right[i]);
    return difference == 0;
}
}

TlsProof::Result TlsProof::verify(const QByteArray &json, const QByteArray &nonce, const QByteArray &token,
                                 const QSslCertificate &peer) {
    const QByteArray normalizedNonce = nonce.toLower();
    if (!hex64(normalizedNonce) || token.isEmpty() || peer.isNull()) return {QSslCertificate(), Failure::InvalidInput};
    QJsonParseError error;
    const auto document = QJsonDocument::fromJson(json, &error);
    const auto object = document.object();
    const QByteArray fingerprint = object.value(QStringLiteral("certificate_sha256")).toString().toLatin1();
    const QByteArray proof = object.value(QStringLiteral("proof")).toString().toLatin1();
    if (error.error != QJsonParseError::NoError || !document.isObject() || !hex64(fingerprint) || !hex64(proof))
        return {QSslCertificate(), Failure::MalformedResponse};
    if (!constantEqual(fingerprint, QCryptographicHash::hash(peer.toDer(), QCryptographicHash::Sha256).toHex()))
        return {QSslCertificate(), Failure::FingerprintMismatch};
    const QByteArray message = QByteArrayLiteral("headroom-tls-proof-v1\n") + normalizedNonce + '\n' + fingerprint;
    const auto expected = QMessageAuthenticationCode::hash(message, token, QCryptographicHash::Sha256).toHex();
    if (!constantEqual(proof, expected)) return {QSslCertificate(), Failure::InvalidProof};
    return {peer, Failure::None};
}
QUrl TlsProof::upgradeUrl(const QUrl &url) {
    QUrl result(url);
    const int port = url.port(url.scheme() == QStringLiteral("http") ? 80 : 443);
    result.setScheme(QStringLiteral("https")); result.setPort(port);
    return result;
}
bool TlsProof::shouldUpgrade(const QUrl &url, bool hasToken, qint64 now, qint64 nextAttempt) {
    return url.scheme() == QStringLiteral("http") && hasToken && now >= nextAttempt;
}
void TlsProofService::cancel() {
    if (m_reply) { m_reply->disconnect(this); m_reply->abort(); m_reply->deleteLater(); m_reply.clear(); }
    m_network.clearConnectionCache();
}
void TlsProofService::request(const QUrl &base, const QByteArray &token, std::function<void(TlsProof::Result)> finished) {
    cancel();
    QByteArray random(32, '\0');
    for (int i = 0; i < 32; i += 4) {
        const quint32 value = QRandomGenerator::system()->generate();
        for (int j = 0; j < 4; ++j) random[i + j] = char(value >> (j * 8));
    }
    const QByteArray nonce = random.toHex();
    QUrl url = Usage::endpoint(TlsProof::upgradeUrl(base).toString());
    QString path = url.path(); path.chop(QStringLiteral("usage").size());
    url.setPath(path + QStringLiteral("tls/proof"));
    QUrlQuery query; query.addQueryItem(QStringLiteral("nonce"), QString::fromLatin1(nonce)); url.setQuery(query);
    QNetworkRequest request(url);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(10000);
    request.setRawHeader("Accept", "application/json");
    QSslConfiguration configuration = QSslConfiguration::defaultConfiguration();
    configuration.setPeerVerifyMode(QSslSocket::VerifyNone);
    configuration.setProtocol(QSsl::TlsV1_2OrLater);
    request.setSslConfiguration(configuration);
    auto reply = m_network.get(request); m_reply = reply;
    reply->setReadBufferSize(4097);
    connect(reply, &QNetworkReply::readyRead, reply, [reply] { if (reply->bytesAvailable() > 4096) reply->abort(); });
    QTimer::singleShot(12000, reply, [reply] { if (!reply->isFinished()) reply->abort(); });
    connect(reply, &QNetworkReply::finished, this, [this, reply, token, nonce, finished] {
        TlsProof::Result result{QSslCertificate(), TlsProof::Failure::Network};
        if (reply->error() == QNetworkReply::NoError
            && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 200
            && !reply->attribute(QNetworkRequest::RedirectionTargetAttribute).isValid())
            result = TlsProof::verify(reply->readAll(), nonce, token, reply->sslConfiguration().peerCertificate());
        m_reply.clear(); reply->deleteLater(); m_network.clearConnectionCache();
        finished(result);
    });
}
