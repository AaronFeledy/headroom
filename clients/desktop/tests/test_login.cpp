#include "tlsproof.h"
#include "logincopy.h"
#include "serverconnection.h"
#include "controller.h"
#include "tls_fixture.h"
#include "usagefixture.h"
#include "http_assertions.h"
#include <QtTest>
#include <QCryptographicHash>
#include <QMessageAuthenticationCode>
#include <QUrlQuery>
#include <QSslServer>
#include <QTemporaryDir>
#include <QScopeGuard>

namespace {
QByteArray proofJson(const QByteArray &nonce, const QByteArray &token, const QSslCertificate &certificate) {
    const auto fingerprint = QCryptographicHash::hash(certificate.toDer(), QCryptographicHash::Sha256).toHex();
    const auto proof = QMessageAuthenticationCode::hash(QByteArrayLiteral("headroom-tls-proof-v1\n")
        + nonce.toLower() + '\n' + fingerprint, token, QCryptographicHash::Sha256).toHex();
    return QJsonDocument(QJsonObject{{"certificate_sha256", QString::fromLatin1(fingerprint)},
        {"proof", QString::fromLatin1(proof)}}).toJson(QJsonDocument::Compact);
}
QVariantMap provider(const QString &state = QStringLiteral("expired"), const QString &kind = QStringLiteral("cli"),
                     const QString &name = QStringLiteral("cursor-agent")) {
    return {{"provider_name", "Cursor"}, {"error", "Synthetic error"}, {"auth", QVariantMap{{"state", state},
        {"source", QVariantMap{{"kind", kind}, {"name", name}}}, {"checked", QVariantList{}}}}};
}
class ProofServer : public QSslServer {
public:
    QList<QByteArray> requests;
    QByteArray token = "synthetic-token";
    bool invalidProof = false;
    explicit ProofServer(bool replacement = false) {
        TlsFixture::configure(*this, replacement);
        connect(this, &QTcpServer::pendingConnectionAvailable, this, [this] {
            while (hasPendingConnections()) {
                auto socket = nextPendingConnection();
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
                const auto read = [this, socket] {
                    const auto bytes = socket->property("request").toByteArray() + socket->readAll();
                    socket->setProperty("request", bytes);
                    if (!bytes.contains("\r\n\r\n") || socket->property("handled").toBool()) return;
                    socket->setProperty("handled", true); requests.append(bytes);
                    const QUrl url(QStringLiteral("https://fixture") + QString::fromLatin1(bytes.split(' ')[1]));
                    QByteArray body;
                    if (url.path().endsWith(QStringLiteral("tls/proof"))) {
                        const auto nonce = QUrlQuery(url).queryItemValue(QStringLiteral("nonce")).toLatin1();
                        body = proofJson(nonce, invalidProof ? QByteArrayLiteral("wrong-token") : token, sslConfiguration().localCertificate());
                    } else body = TestUsage::snapshot();
                    socket->write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
                        + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                    socket->disconnectFromHost();
                };
                connect(socket, &QTcpSocket::readyRead, socket, read);
                QMetaObject::invokeMethod(socket, read, Qt::QueuedConnection);
            }
        });
    }
    QString url(const QString &scheme = QStringLiteral("https")) const {
        return QStringLiteral("%1://127.0.0.2:%2/base").arg(scheme).arg(serverPort());
    }
};
}

class LoginTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() { QVERIFY(TlsFixture::selectNativeTestBackend()); }
    void proofVerification() {
        const QByteArray nonce(64, 'a'), token("synthetic-token");
        const auto certificate = TlsFixture::certificate();
        const auto valid = proofJson(nonce, token, certificate);
        QVERIFY(TlsProof::verify(valid, nonce, token, certificate).verified());
        QVERIFY(TlsProof::verify(valid, nonce.toUpper(), token, certificate).verified());
        QCOMPARE(TlsProof::verify(valid, nonce, "wrong", certificate).failure, TlsProof::Failure::InvalidProof);
        QCOMPARE(TlsProof::verify(valid, nonce, token, TlsFixture::replacementCertificate()).failure, TlsProof::Failure::FingerprintMismatch);
        for (const auto &malformed : {QByteArray("not-json"), QByteArray("[]"), QByteArray("{}"), QByteArray("{\"proof\":42}")})
            QCOMPARE(TlsProof::verify(malformed, nonce, token, certificate).failure, TlsProof::Failure::MalformedResponse);
        QCOMPARE(TlsProof::verify(valid, "bad", token, certificate).failure, TlsProof::Failure::InvalidInput);
    }
    void upgradeDecisions() {
        const QUrl http(QStringLiteral("http://example.test/base"));
        QCOMPARE(TlsProof::upgradeUrl(http).toString(), QStringLiteral("https://example.test:80/base"));
        QCOMPARE(TlsProof::upgradeUrl(QUrl("http://example.test:7823")).port(), 7823);
        QVERIFY(TlsProof::shouldUpgrade(http, true, 10, 0));
        QVERIFY(!TlsProof::shouldUpgrade(http, false, 10, 0));
        QVERIFY(!TlsProof::shouldUpgrade(QUrl("https://example.test"), true, 10, 0));
        QVERIFY(!TlsProof::shouldUpgrade(http, true, 6 * 3600 - 1, 6 * 3600));
        QVERIFY(TlsProof::shouldUpgrade(http, true, 6 * 3600, 6 * 3600));
    }
    void remotePinConfiguration() {
        QNetworkRequest request(QUrl("https://example.test"));
        const auto certificate = TlsFixture::certificate();
        ServerTransport::secureRequest(request, certificate);
        const auto configuration = request.sslConfiguration();
        QCOMPARE(configuration.caCertificates(), QList<QSslCertificate>{certificate});
        QCOMPARE(configuration.peerVerifyMode(), QSslSocket::VerifyPeer);
        QCOMPARE(configuration.peerVerifyDepth(), 1);
    }
    void failedUpgradeStaysHttpAndBacksOff() {
        QTcpServer server; QVERIFY(server.listen(QHostAddress::LocalHost));
        int proofs = 0, usage = 0;
        connect(&server, &QTcpServer::newConnection, this, [&] {
            auto socket = server.nextPendingConnection();
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
            connect(socket, &QTcpSocket::readyRead, socket, [&, socket] {
                const auto bytes = socket->readAll();
                if (!bytes.isEmpty() && bytes[0] == char(0x16)) { ++proofs; socket->disconnectFromHost(); return; }
                if (!bytes.contains("\r\n\r\n")) return;
                ++usage; const auto body = TestUsage::snapshot();
                socket->write("HTTP/1.1 200 OK\r\nContent-Length: " + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            });
        });
        QTemporaryDir dir; Controller controller(dir.filePath("settings.json"), nullptr, false, {}, {}, {}, false);
        const auto url = QStringLiteral("http://127.0.0.1:%1").arg(server.serverPort());
        QVERIFY(controller.saveSettings("remote", url, "synthetic-token", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QStringLiteral("ready"));
        QCOMPARE(proofs, 1); QCOMPARE(usage, 1); QCOMPARE(controller.backendUrl(), url);
        QVERIFY(!controller.remoteVerificationPending());
        controller.refresh(); QTRY_COMPARE(usage, 2); QTRY_VERIFY(!controller.state()["loading"].toBool()); QCOMPARE(proofs, 1);
        QVERIFY(controller.saveSettings("remote", url, "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(usage, 3); QCOMPARE(proofs, 1);
        QVERIFY(controller.saveSettings("remote", url, "synthetic-token2", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(usage, 4); QCOMPARE(proofs, 2);
    }
    void upgradeAndUntrustedHttps_data() {
        QTest::addColumn<QString>("scheme");
        QTest::newRow("upgrade") << QStringLiteral("http");
        QTest::newRow("self-signed-https") << QStringLiteral("https");
    }
    void systemTrustedHttpsNeedsNoProofOrPin() {
        const auto original = QSslConfiguration::defaultConfiguration();
        const auto restore = qScopeGuard([original] { QSslConfiguration::setDefaultConfiguration(original); });
        auto trusted = original; trusted.setCaCertificates({TlsFixture::certificate()});
        QSslConfiguration::setDefaultConfiguration(trusted);
        ProofServer server; QVERIFY(server.listen(QHostAddress::AnyIPv4));
        QUrl url(server.url()); url.setHost(QStringLiteral("127.0.0.1"));
        QTemporaryDir dir; SettingsService settings(dir.filePath("settings.json"), false);
        auto value = settings.value(); value.connectionMode = "remote"; value.url = url.toString(); value.token = QString::fromUtf8(server.token);
        QVERIFY(settings.save(value).isEmpty());
        Controller controller(settings.path(), nullptr, false, {}, {}, {}, false); controller.refresh();
        QTRY_COMPARE(controller.state()["status"].toString(), QStringLiteral("ready"));
        QCOMPARE(server.requests.size(), 1); QVERIFY(server.requests.first().startsWith("GET /base/api/v1/usage "));
        QVERIFY(HttpAssertions::hasHeader(server.requests.first(), "Authorization", "Bearer synthetic-token"));
        QVERIFY(controller.backendCertificate().isNull());
        QCOMPARE(controller.settings()["connectionStatus"].toString(), QStringLiteral("Encrypted."));
    }
    void upgradeAndUntrustedHttps() {
        QFETCH(QString, scheme);
        ProofServer server; QVERIFY(server.listen(QHostAddress::AnyIPv4));
        QTemporaryDir dir; SettingsService settings(dir.filePath("settings.json"), false);
        auto value = settings.value(); value.connectionMode = "remote"; value.url = server.url(scheme); value.token = QString::fromUtf8(server.token);
        QVERIFY(settings.save(value).isEmpty());
        Controller controller(settings.path(), nullptr, false, {}, {}, {}, false);
        QCOMPARE(controller.remoteVerificationPending(), scheme == QStringLiteral("http"));
        controller.refresh();
        QTRY_COMPARE(controller.state()["status"].toString(), QStringLiteral("ready"));
        QVERIFY(!controller.remoteVerificationPending());
        QCOMPARE(server.requests.size(), 2);
        QVERIFY(server.requests[0].startsWith("GET /base/api/v1/tls/proof?nonce="));
        QVERIFY(!HttpAssertions::hasHeader(server.requests[0], "Authorization"));
        QVERIFY(HttpAssertions::hasHeader(server.requests[1], "Authorization", "Bearer synthetic-token"));
        QCOMPARE(controller.backendCertificate().toDer(), TlsFixture::certificate().toDer());
        QCOMPARE(QUrl(controller.backendUrl()).scheme(), QStringLiteral("https"));
        SettingsService saved(settings.path(), false); QVERIFY(!saved.value().remoteCertificate.isEmpty());
        if (scheme == QStringLiteral("http")) QVERIFY(controller.diagnosticText().contains(QStringLiteral("Connection upgraded to HTTPS.")));
    }
    void rotationVerifiedOrFailsClosed_data() {
        QTest::addColumn<bool>("valid"); QTest::newRow("verified") << true; QTest::newRow("wrong-token") << false;
    }
    void rotationVerifiedOrFailsClosed() {
        QFETCH(bool, valid);
        ProofServer server(true); server.invalidProof = !valid; QVERIFY(server.listen(QHostAddress::AnyIPv4));
        QTemporaryDir dir; SettingsService settings(dir.filePath("settings.json"), false);
        auto value = settings.value(); value.connectionMode = "remote"; value.url = server.url(); value.token = QString::fromUtf8(server.token);
        QVERIFY(settings.save(value).isEmpty()); value.remoteCertificate = QString::fromUtf8(TlsFixture::certificate().toPem());
        QVERIFY(settings.save(value).isEmpty());
        Controller controller(settings.path(), nullptr, false, {}, {}, {}, false); controller.refresh();
        QTRY_VERIFY(controller.state()["status"].toString() == (valid ? QStringLiteral("ready") : QStringLiteral("offline")));
        QCOMPARE(server.requests.size(), valid ? 2 : 1);
        QVERIFY(!HttpAssertions::hasHeader(server.requests.first(), "Authorization"));
        if (valid) {
            QCOMPARE(controller.backendCertificate().toDer(), TlsFixture::replacementCertificate().toDer());
            QVERIFY(controller.diagnosticText().contains(QStringLiteral("The server certificate changed and was verified with your access token.")));
        } else {
            QCOMPARE(controller.backendCertificate().toDer(), TlsFixture::certificate().toDer());
            QCOMPARE(controller.state()["errorKind"].toString(), QStringLiteral("certificate"));
            controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
            QCOMPARE(server.requests.size(), 1);
        }
        QCOMPARE(QUrl(controller.backendUrl()).scheme(), QStringLiteral("https"));
    }
    void firstLineRules_data() {
        QTest::addColumn<QString>("kind"); QTest::addColumn<QString>("name"); QTest::addColumn<QString>("expected");
        const auto row = [](const char *label, const char *kind, const char *name, const char *expected) {
            QTest::newRow(label) << QString::fromUtf8(kind) << QString::fromUtf8(name) << QString::fromUtf8(expected);
        };
        row("cli", "cli", "cursor-agent", "Your cursor-agent login on the server expired.");
        row("app", "app", "Cursor", "Your Cursor app sign-in on the server expired.");
        row("browser", "browser", "Firefox", "Your Firefox sign-in to cursor.com on the server expired.");
        row("desktop", "desktop", "Firefox", "The Firefox sign-in shared by Headroom expired.");
        row("desktop-unknown", "desktop", "browser", "The browser sign-in shared by Headroom expired.");
        row("api", "api", "API", "The credential sent to the server expired.");
        row("codex-name", "cli", "Codex", "Your ChatGPT login on the server expired.");
    }
    void firstLineRules() {
        QFETCH(QString, kind); QFETCH(QString, name); QFETCH(QString, expected);
        const auto p = provider("expired", kind, name);
        auto copy = LoginCopy::compose(p, "remote", "available");
        QCOMPARE(copy["title"].toString(), QStringLiteral("Sign-in expired"));
        QCOMPARE(copy["lines"].toStringList().first(), expected);
        copy = LoginCopy::compose(p, "local", "available");
        QCOMPARE(copy["lines"].toStringList().first(), expected.replace("on the server", "on this computer"));
    }
    void browserCopyRules_data() {
        QTest::addColumn<QString>("mode"); QTest::addColumn<QString>("sharing"); QTest::addColumn<bool>("checked");
        QTest::addColumn<QString>("line"); QTest::addColumn<bool>("open");
        const auto row = [](const char *label, const char *mode, const char *sharing, bool checked, const char *line, bool open) {
            QTest::newRow(label) << QString::fromUtf8(mode) << QString::fromUtf8(sharing) << checked << QString::fromUtf8(line) << open;
        };
        row("local", "local", "available", false, "Or sign in to cursor.com in your browser.", true);
        row("remote", "remote", "available", false, "Or sign in to cursor.com in a browser on this computer. Headroom shares it with the server.", true);
        row("ssh", "ssh", "available", false, "Or sign in to cursor.com in a browser on this computer. Headroom shares it with the server.", true);
        row("local-unavailable", "local", "unavailable", false, "Or sign in to cursor.com in Firefox.", true);
        row("insecure", "remote", "insecure", false, "Browser sign-ins on this computer can't be shared over an unencrypted connection. Update the server to turn on HTTPS, or connect with SSH.", false);
        row("disabled", "remote", "disabled", false, "Sharing browser sign-ins is turned off in Settings.", false);
        row("unsupported-server-browser", "remote", "unsupported", true, "Or sign in to cursor.com in Firefox on the server.", false);
        row("unavailable-server-browser", "remote", "unavailable", true, "Or sign in to cursor.com in Firefox on the server.", false);
        row("unsupported-no-browser", "remote", "unsupported", false, "", false);
        row("unavailable-no-browser", "remote", "unavailable", false, "", false);
    }
    void browserCopyRules() {
        QFETCH(QString, mode); QFETCH(QString, sharing); QFETCH(bool, checked); QFETCH(QString, line); QFETCH(bool, open);
        auto p = provider("signed_out"); auto auth = p["auth"].toMap(); auth["source"] = QVariant();
        auth["sign_in_url"] = "https://cursor.com/login";
        auth["sign_in_command"] = "cursor-agent login";
        if (checked) auth["checked"] = QVariantList{QVariantMap{{"kind", "browser"}, {"name", "Firefox"}, {"status", "signed_out"}}};
        p["auth"] = auth;
        const auto copy = LoginCopy::compose(p, mode, sharing);
        QCOMPARE(copy["title"].toString(), QStringLiteral("Not signed in"));
        const auto lines = copy["lines"].toStringList();
        QCOMPARE(lines.first(), QStringLiteral("Headroom couldn't find a Cursor sign-in."));
        QCOMPARE(lines[1], mode == "local" ? QStringLiteral("Run cursor-agent login on this computer.") : QStringLiteral("Run cursor-agent login on the server."));
        QCOMPARE(lines.size(), line.isEmpty() ? 2 : 3);
        if (!line.isEmpty()) QCOMPARE(lines.last(), line);
        QCOMPARE(!copy["url"].toString().isEmpty(), open);
        QCOMPARE(copy["command"].toString(), QStringLiteral("cursor-agent login"));
    }
    void checkedNotes_data() {
        QTest::addColumn<QString>("status"); QTest::addColumn<QString>("expected");
        QTest::newRow("encrypted") << QStringLiteral("encrypted") << QStringLiteral("Chrome on the server is signed in, but Headroom can't read its cookies.");
        QTest::newRow("locked") << QStringLiteral("locked") << QStringLiteral("Close Chrome on the server so Headroom can read its sign-in, or use another browser.");
        QTest::newRow("expired") << QStringLiteral("expired") << QStringLiteral("Your Chrome on the server sign-in also expired.");
        QTest::newRow("unreadable") << QStringLiteral("unreadable") << QStringLiteral("Headroom couldn't read Chrome on the server.");
    }
    void checkedNotes() {
        QFETCH(QString, status); QFETCH(QString, expected);
        auto p = provider(); auto auth = p["auth"].toMap();
        auth["checked"] = QVariantList{QVariantMap{{"kind", "cli"}, {"name", "cursor-agent"}, {"status", "expired"}},
            QVariantMap{{"kind", "browser"}, {"name", "Firefox"}, {"status", "signed_in"}},
            QVariantMap{{"kind", "browser"}, {"name", "Edge"}, {"status", "signed_out"}},
            QVariantMap{{"kind", "browser"}, {"name", "Chrome"}, {"status", status}}}; p["auth"] = auth;
        const QVariantList helper{QVariantMap{{"name", "Brave"}, {"status", "unreadable"}}, QVariantMap{{"name", "Edge"}, {"status", "locked"}}};
        auto lines = LoginCopy::compose(p, "remote", "available", helper)["lines"].toStringList();
        QCOMPARE(lines.size(), 3); QCOMPARE(lines[1], expected); QCOMPARE(lines[2], QStringLiteral("Headroom couldn't read Brave."));
        lines = LoginCopy::compose(p, "local", "available", helper)["lines"].toStringList();
        QCOMPARE(lines[1], expected.replace(" on the server", ""));
    }
    void legacyAndSignedInErrors() {
        auto p = provider(); p.remove("auth"); QVERIFY(LoginCopy::compose(p, "local", "available").isEmpty());
        p = provider("signed_in"); p["needs_reauth"] = true;
        const auto copy = LoginCopy::compose(p, "remote", "available");
        QCOMPARE(copy["title"].toString(), QStringLiteral("Usage is unavailable"));
        QCOMPARE(copy["lines"].toStringList(), QStringList{QStringLiteral("Synthetic error")});
        auto auth = p["auth"].toMap(); auth["state"] = "signed_out"; auth["sign_in_url"] = "javascript:alert(1)";
        auth["sign_in_command"] = "login\nunsafe"; p["auth"] = auth;
        const auto invalid = LoginCopy::compose(p, "remote", "available");
        QVERIFY(invalid["url"].toString().isEmpty()); QVERIFY(invalid["command"].toString().isEmpty());
        p["provider_name"] = "Codex"; QCOMPARE(LoginCopy::compose(p, "local", "available")["lines"].toStringList().first(), QStringLiteral("Headroom couldn't find a ChatGPT sign-in."));
    }
};
QTEST_GUILESS_MAIN(LoginTest)
#include "test_login.moc"
