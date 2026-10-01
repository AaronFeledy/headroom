#pragma once

#include <QObject>
#include <QNetworkAccessManager>
#include <QPointer>
#include <QNetworkReply>
#include <QSslCertificate>
#include <functional>

namespace TlsProof {
enum class Failure { None, InvalidInput, MalformedResponse, FingerprintMismatch, InvalidProof, Network };
struct Result {
    QSslCertificate certificate;
    Failure failure = Failure::None;
    bool verified() const { return failure == Failure::None && !certificate.isNull(); }
};
Result verify(const QByteArray &json, const QByteArray &nonce, const QByteArray &token,
              const QSslCertificate &peer);
QUrl upgradeUrl(const QUrl &url);
bool shouldUpgrade(const QUrl &url, bool hasToken, qint64 now, qint64 nextAttempt);
}

// This manager is exclusively for unauthenticated proof requests.
class TlsProofService : public QObject {
public:
    explicit TlsProofService(QObject *parent = nullptr) : QObject(parent) {}
    void request(const QUrl &base, const QByteArray &token, std::function<void(TlsProof::Result)> finished);
    void cancel();
private:
    QNetworkAccessManager m_network;
    QPointer<QNetworkReply> m_reply;
};
