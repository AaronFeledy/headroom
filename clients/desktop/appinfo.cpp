#include "appinfo.h"
#include "serverconnection.h"
#include "usage.h"
#include <QCoreApplication>
#include <QHostAddress>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkProxy>
#include <QRegularExpression>
#include <QTimer>

namespace {
QString safeVersion(const QJsonValue &value) {
    const QString version = value.toString();
    static const QRegularExpression valid("^[A-Za-z0-9][A-Za-z0-9.+_-]{0,79}$");
    return valid.match(version).hasMatch() ? version : QString();
}
bool success(QNetworkReply *reply) {
    return reply->error() == QNetworkReply::NoError
        && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 200;
}
// Compare SemVer without converting untrusted numeric fields to bounded integers.
// Build metadata does not change precedence; development labels are not versions.
int numericCompare(const QString &left, const QString &right) {
    if (left.size() != right.size()) return left.size() < right.size() ? -1 : 1;
    return QString::compare(left, right, Qt::CaseSensitive);
}
bool newerVersion(const QString &left, const QString &right) {
    static const QRegularExpression semver(QStringLiteral(
        "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)"
        "(?:-([0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*))?"
        "(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$"));
    static const QRegularExpression numeric(QStringLiteral("^[0-9]+$"));
    const auto a = semver.match(left), b = semver.match(right);
    if (!a.hasMatch() || !b.hasMatch()) return false;
    const auto aPre = a.captured(4).split('.'), bPre = b.captured(4).split('.');
    for (const auto &list : {aPre, bPre})
        for (const auto &identifier : list)
            if (numeric.match(identifier).hasMatch() && identifier.size() > 1 && identifier.startsWith('0')) return false;
    for (int part = 1; part <= 3; ++part) {
        const int comparison = numericCompare(a.captured(part), b.captured(part));
        if (comparison) return comparison > 0;
    }
    if (a.captured(4).isEmpty() || b.captured(4).isEmpty())
        return a.captured(4).isEmpty() && !b.captured(4).isEmpty();
    for (qsizetype index = 0; index < qMin(aPre.size(), bPre.size()); ++index) {
        const bool aNumeric = numeric.match(aPre[index]).hasMatch(), bNumeric = numeric.match(bPre[index]).hasMatch();
        const int comparison = aNumeric != bNumeric ? (aNumeric ? -1 : 1)
            : aNumeric ? numericCompare(aPre[index], bPre[index]) : QString::compare(aPre[index], bPre[index], Qt::CaseSensitive);
        if (comparison) return comparison > 0;
    }
    return aPre.size() > bPre.size();
}
}
AppInfo::AppInfo(QObject *parent, int timeoutMs, SshOptions sshOptions)
    : QObject(parent), m_sshNetwork(std::move(sshOptions)), m_timeoutMs(timeoutMs)
{ m_localNetwork.setProxy(QNetworkProxy::NoProxy); }
AppInfo::~AppInfo() {
    // The network manager is destroyed after the fields the health handler writes, so an
    // in-flight reply must be disconnected and aborted first.
    if (m_healthReply) {
        auto reply = m_healthReply; m_healthReply = nullptr;
        reply->disconnect(this); reply->abort();
    }
}
QString AppInfo::applicationVersion() const {
    return QCoreApplication::applicationVersion();
}
QString AppInfo::serverUpdateNotice() const {
    if (!m_remote || !newerVersion(applicationVersion(), m_serverVersion)) return {};
    if (m_healthUrl.scheme() == QStringLiteral("ssh"))
        return QStringLiteral("Your desktop is newer than the remote server (%1). Use Update server in About & Updates, or run headroom update on the server.")
            .arg(m_serverVersion);
    return QStringLiteral("Your desktop is newer than the remote server (%1). Run headroom update on the server's machine to update it.")
        .arg(m_serverVersion);
}
QNetworkReply *AppInfo::request(const QUrl &url, const QByteArray &token, const QSslCertificate &certificate) {
    QNetworkRequest request(url);
    // Manual policy rejects every redirect, including same-origin redirects. In
    // particular a backend cannot redirect the bearer token to a different host.
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setRawHeader("User-Agent", "Headroom/" + applicationVersion().toUtf8());
    request.setRawHeader("Accept", "application/json");
    if (!token.isEmpty()) request.setRawHeader("Authorization", "Bearer " + token);
    ServerTransport::secureRequest(request, certificate);
    QHostAddress address;
    const bool local = !certificate.isNull() || (address.setAddress(url.host()) && address.isLoopback());
    QNetworkAccessManager *network = url.scheme() == QStringLiteral("ssh") ? static_cast<QNetworkAccessManager *>(&m_sshNetwork)
        : local ? &m_localNetwork : &m_network;
    auto reply = network->get(request);
    ServerTransport::requirePinnedPeer(reply, certificate);
    reply->setReadBufferSize(1024 * 1024 + 1);
    connect(reply, &QIODevice::readyRead, reply, [reply] {
        if (reply->bytesAvailable() > 1024 * 1024) reply->abort();
    });
    QTimer::singleShot(url.scheme() == QStringLiteral("ssh") ? qMax(m_timeoutMs, 27000) : m_timeoutMs,
                       reply, [reply] { if (!reply->isFinished()) reply->abort(); });
    return reply;
}
void AppInfo::setBackend(const QString &baseUrl, const QString &token, const QSslCertificate &certificate, bool remote) {
    auto endpoint = Usage::endpoint(baseUrl);
    if (!endpoint.isEmpty()) {
        QString path = endpoint.path(); path.chop(QString("usage").size());
        endpoint.setPath(path + "health");
    }
    const QByteArray effectiveToken = endpoint.scheme() == QStringLiteral("ssh") ? QByteArray() : token.toUtf8();
    const bool remoteChanged = m_remote != remote;
    m_remote = remote;
    if (endpoint == m_healthUrl && effectiveToken == m_token && certificate == m_certificate) {
        if (remoteChanged) emit changed();
        return;
    }
    if (m_healthReply) {
        auto previous = m_healthReply; m_healthReply = nullptr;
        previous->disconnect(this); previous->abort(); previous->deleteLater();
    }
    m_localNetwork.clearConnectionCache();
    m_healthUrl = endpoint; m_token = effectiveToken; m_certificate = certificate; m_serverVersion.clear();
    m_serverStatus = endpoint.isEmpty() ? "Connect a backend to see its version." : "Server version has not been checked.";
    emit changed();
    emit backendChanged();
}
void AppInfo::refreshServer() {
    if (m_healthUrl.isEmpty() || m_healthReply) return;
    auto reply = request(m_healthUrl, m_token, m_certificate); m_healthReply = reply;
    m_serverStatus = "Checking server…"; emit changed();
    connect(reply, &QNetworkReply::finished, this, [this, reply] {
        m_healthReply = nullptr; m_serverVersion.clear();
        const int code = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        if (success(reply)) {
            const auto health = QJsonDocument::fromJson(reply->readAll()).object();
            m_serverVersion = safeVersion(health["version"]);
            if (!m_token.isEmpty() && m_serverVersion.contains(QString::fromUtf8(m_token))) m_serverVersion.clear();
            const auto status = health["status"].toString();
            if (m_serverVersion.isEmpty() || (status != "ok" && status != "degraded")) {
                m_serverVersion.clear(); m_serverStatus = "Server returned unrecognized version details.";
            } else m_serverStatus = status == "ok" ? "Server healthy" : "Server reachable · some providers need attention";
        } else if (code == 401 || code == 403) m_serverStatus = "Server rejected the saved bearer token.";
        else if (code >= 300 && code < 400) m_serverStatus = "Server redirected the version request. Check its address.";
        else m_serverStatus = "Server version unavailable. Try again when connected.";
        reply->deleteLater(); emit changed();
    });
}
