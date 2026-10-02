#include "appinfo.h"
#include "http_assertions.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QTcpServer>
#include <QTcpSocket>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkProxy>
#include <QScopeGuard>

template<typename Server>
class ResponseFixture : public Server {
public:
    QByteArray body = R"({"status":"ok","version":"1.7.1"})";
    QByteArray headers;
    int status = 200;
    bool respond = true;
    QList<QByteArray> requests;
    QList<QPointer<QTcpSocket>> sockets;
    QString scheme = "http";
    ResponseFixture() {
        QObject::connect(this, &QTcpServer::pendingConnectionAvailable, this, [this] {
            while (this->hasPendingConnections()) {
                auto socket = this->nextPendingConnection();
                sockets.append(socket);
                QObject::connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
                auto handle = [this, socket] {
                    auto bytes = socket->property("bytes").toByteArray() + socket->readAll();
                    socket->setProperty("bytes", bytes);
                    if (!bytes.contains("\r\n\r\n") || socket->property("handled").toBool()) return;
                    socket->setProperty("handled", true); requests.append(bytes);
                    if (!respond) return;
                    sendResponse(socket);
                };
                QObject::connect(socket, &QTcpSocket::readyRead, socket, handle);
                if (socket->bytesAvailable()) handle();
            }
        });
    }
    void sendResponse(QTcpSocket *socket) {
        socket->write("HTTP/1.1 " + QByteArray::number(status) + " Result\r\nContent-Type: application/json\r\nContent-Length: "
            + QByteArray::number(body.size()) + "\r\nConnection: close\r\n" + headers + "\r\n" + body);
        socket->disconnectFromHost();
    }
    QString url() const { return QString("%1://127.0.0.1:%2").arg(scheme).arg(this->serverPort()); }
};
using HttpFixture = ResponseFixture<QTcpServer>;
class HttpsFixture : public ResponseFixture<QSslServer> {
public:
    HttpsFixture() { scheme = "https"; TlsFixture::configure(*this); }
};
class AppInfoTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
        QCoreApplication::setApplicationVersion("0.1.0");
    }
    void healthAndRedaction() {
        HttpFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        AppInfo info;
        QCOMPARE(info.applicationVersion(), QString("0.1.0"));
        info.setBackend(fixture.url() + "/prefix/", "private-fixture-token"); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QString("1.7.1")); QCOMPARE(info.serverStatus(), QString("Server healthy"));
        QVERIFY(fixture.requests.last().startsWith("GET /prefix/api/v1/health "));
        QVERIFY(HttpAssertions::hasHeader(fixture.requests.last(), "Authorization",
                                          "Bearer private-fixture-token"));
        fixture.body = R"({"status":"degraded","version":"dev-test"})";
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QString("dev-test")); QVERIFY(info.serverStatus().contains("need attention"));
        fixture.status = 401; fixture.body = "private-fixture-token";
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverVersion().isEmpty()); QVERIFY(info.serverStatus().contains("rejected"));
        QVERIFY(!info.serverStatus().contains("private-fixture-token"));
        fixture.status = 200; fixture.body = R"({"status":"ok","version":"<b>invalid</b>"})";
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverVersion().isEmpty()); QVERIFY(info.serverStatus().contains("unrecognized"));
        fixture.body = R"({"status":"ok","version":"private-fixture-token"})";
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverVersion().isEmpty()); QVERIFY(!info.serverStatus().contains("private-fixture-token"));
        const int count = fixture.requests.size();
        info.setBackend(fixture.url() + "/prefix/", "private-fixture-token");
        QTest::qWait(20); QCOMPARE(fixture.requests.size(), count);
        info.setBackend("https://user:secret@example.org/", "token"); info.refreshServer();
        QVERIFY(!info.checkingServer()); QVERIFY(info.serverVersion().isEmpty());
    }
    void pinnedSessionRejectsReplacementBeforeSendingToken() {
        HttpsFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        AppInfo info;
        info.setBackend(fixture.url(), "first-session-token", TlsFixture::certificate());
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QString("1.7.1"));
        QCOMPARE(fixture.requests.size(), 1);
        QVERIFY(HttpAssertions::hasHeader(fixture.requests.first(), "Authorization", "Bearer first-session-token"));

        // A new, otherwise valid TLS identity must receive no HTTP request under the old pin.
        fixture.setSslConfiguration(TlsFixture::replacementServerConfiguration());
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverVersion().isEmpty());
        QCOMPARE(fixture.requests.size(), 1);
        QVERIFY(!info.serverStatus().contains("first-session-token"));

        info.setBackend(fixture.url(), "second-session-token", TlsFixture::replacementCertificate());
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QString("1.7.1"));
        QCOMPARE(fixture.requests.size(), 2);
        QVERIFY(HttpAssertions::hasHeader(fixture.requests.last(), "Authorization", "Bearer second-session-token"));
        QVERIFY(!fixture.requests.last().contains("first-session-token"));
    }
    void remotePinnedHealthUsesConfiguredProxy() {
        HttpsFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        QTcpServer proxy; QVERIFY(proxy.listen(QHostAddress::LocalHost));
        int tunnels = 0;
        connect(&proxy, &QTcpServer::newConnection, this, [&] {
            auto socket = proxy.nextPendingConnection();
            connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
            connect(socket, &QTcpSocket::readyRead, socket, [&, socket] {
                if (socket->property("tunneling").toBool()) return;
                auto bytes = socket->property("request").toByteArray() + socket->readAll();
                socket->setProperty("request", bytes);
                if (!bytes.contains("\r\n\r\n")) return;
                QVERIFY(bytes.startsWith("CONNECT headroom-proxy-fixture.invalid:"));
                QVERIFY(!bytes.contains("private-fixture-token"));
                socket->setProperty("tunneling", true); ++tunnels;
                auto upstream = new QTcpSocket(socket);
                upstream->setProxy(QNetworkProxy::NoProxy);
                connect(upstream, &QTcpSocket::connected, socket, [socket] {
                    socket->write("HTTP/1.1 200 Connection Established\r\n\r\n");
                });
                connect(upstream, &QTcpSocket::readyRead, socket, [socket, upstream] { socket->write(upstream->readAll()); });
                connect(socket, &QTcpSocket::readyRead, upstream, [socket, upstream] { upstream->write(socket->readAll()); });
                connect(upstream, &QTcpSocket::disconnected, socket, &QTcpSocket::disconnectFromHost);
                upstream->connectToHost(QHostAddress::LocalHost, fixture.serverPort());
            });
        });
        const auto previous = QNetworkProxy::applicationProxy();
        const auto restore = qScopeGuard([previous] { QNetworkProxy::setApplicationProxy(previous); });
        QNetworkProxy::setApplicationProxy(QNetworkProxy(QNetworkProxy::HttpProxy, "127.0.0.1", proxy.serverPort()));
        AppInfo info(nullptr, 2000);
        QUrl url(fixture.url()); url.setHost("headroom-proxy-fixture.invalid");
        info.setBackend(url.toString(), "private-fixture-token", TlsFixture::certificate(), true);
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(tunnels, 1);
        QCOMPARE(info.serverVersion(), QString("1.7.1"));
        QCOMPARE(fixture.requests.size(), 1);
        QVERIFY(HttpAssertions::hasHeader(fixture.requests.first(), "Authorization", "Bearer private-fixture-token"));
    }
    void changingPrivateSessionCancelsPendingVersionRequest() {
        HttpsFixture slow, next;
        QVERIFY(slow.listen(QHostAddress::LocalHost)); QVERIFY(next.listen(QHostAddress::LocalHost));
        slow.respond = false;
        AppInfo info;
        info.setBackend(slow.url(), "retired-session-token", TlsFixture::certificate());
        info.refreshServer(); QTRY_COMPARE(slow.requests.size(), 1);
        QVERIFY(info.checkingServer());
        const QPointer<QTcpSocket> pending = slow.sockets.last();
        info.setBackend(next.url(), "current-session-token", TlsFixture::certificate());
        QVERIFY(!info.checkingServer());
        slow.body = R"({"status":"ok","version":"stale-version"})";
        if (pending && pending->state() == QAbstractSocket::ConnectedState) slow.sendResponse(pending);
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QString("1.7.1"));
        QCOMPARE(next.requests.size(), 1);
        QVERIFY(HttpAssertions::hasHeader(next.requests.first(), "Authorization", "Bearer current-session-token"));
        QVERIFY(!next.requests.first().contains("retired-session-token"));
    }
    void redirectsNeverForwardToken() {
        HttpFixture origin, destination;
        QVERIFY(origin.listen(QHostAddress::LocalHost)); QVERIFY(destination.listen(QHostAddress::LocalHost));
        origin.status = 302; origin.headers = "Location: " + destination.url().toUtf8() + "/health\r\n";
        AppInfo info; info.setBackend(origin.url(), "private-fixture-token"); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverStatus().contains("redirected")); QCOMPARE(destination.requests.size(), 0);
    }
    void timeoutsAndBackendSwitch() {
        HttpFixture slow, next; QVERIFY(slow.listen(QHostAddress::LocalHost)); QVERIFY(next.listen(QHostAddress::LocalHost));
        slow.respond = false;
        AppInfo info(nullptr, 100);
        info.setBackend(slow.url(), "old-token"); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer()); QVERIFY(info.serverStatus().contains("unavailable"));
        info.refreshServer(); QTRY_VERIFY(slow.requests.size() >= 2);
        info.setBackend(next.url(), "new-token"); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer()); QCOMPARE(info.serverVersion(), QString("1.7.1"));
        QVERIFY(HttpAssertions::hasHeader(next.requests.last(), "Authorization", "Bearer new-token"));
        QVERIFY(!next.requests.last().contains("old-token"));
    }
    void rejectsOversizedResponses() {
        HttpFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        fixture.body = QByteArray(1024 * 1024 + 100, 'x');
        AppInfo info(nullptr, 2000);
        info.setBackend(fixture.url(), "test-token"); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer()); QVERIFY(info.serverVersion().isEmpty());
    }
    void checksVersionOverSsh()
    {
        AppInfo info(nullptr, 1000, SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 1000});
        info.setBackend(QStringLiteral("ssh://valid"), QStringLiteral("must-not-be-sent"));
        info.refreshServer();
        QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(info.serverVersion(), QStringLiteral("test"));
        QCOMPARE(info.serverStatus(), QStringLiteral("Server healthy"));
    }
    void remoteUpdateNoticeUsesSemanticVersions_data() {
        QTest::addColumn<QString>("desktop");
        QTest::addColumn<QString>("server");
        QTest::addColumn<bool>("warn");
        QTest::newRow("newer") << "2.0.0" << "1.9.9" << true;
        QTest::newRow("numeric") << "1.10.0" << "1.9.0" << true;
        QTest::newRow("same") << "1.2.3" << "1.2.3" << false;
        QTest::newRow("older-desktop") << "1.2.3" << "2.0.0" << false;
        QTest::newRow("build-metadata") << "1.2.3+new" << "1.2.3+old" << false;
        QTest::newRow("release") << "1.2.3" << "1.2.3-rc.1" << true;
        QTest::newRow("prerelease") << "1.2.3-rc.1" << "1.2.3" << false;
        QTest::newRow("prerelease-numeric") << "1.2.3-rc.10" << "1.2.3-rc.9" << true;
        QTest::newRow("prerelease-segments") << "1.2.3-rc.1" << "1.2.3-rc" << true;
        QTest::newRow("dev") << "dev" << "1.0.0" << false;
        QTest::newRow("unknown-server") << "2.0.0" << "dev" << false;
        QTest::newRow("leading-zero") << "2.0.0" << "01.0.0" << false;
        QTest::newRow("bad-prerelease") << "2.0.0" << "1.0.0-01" << false;
    }
    void remoteUpdateNoticeUsesSemanticVersions() {
        QFETCH(QString, desktop); QFETCH(QString, server); QFETCH(bool, warn);
        QCoreApplication::setApplicationVersion(desktop);
        HttpFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        fixture.body = QJsonDocument(QJsonObject{{"status", "ok"}, {"version", server}}).toJson();
        AppInfo info;
        info.setBackend(fixture.url(), {}, QSslCertificate(), true); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer());
        QCOMPARE(!info.serverUpdateNotice().isEmpty(), warn);
        if (warn) QVERIFY(info.serverUpdateNotice().contains("headroom update"));
        // Local ownership is an explicit connection property, never a hostname inference.
        info.setBackend(fixture.url(), {}, QSslCertificate(), false);
        QVERIFY(info.serverUpdateNotice().isEmpty());
        QCoreApplication::setApplicationVersion("0.1.0");
    }
    void remoteUpdateNoticeClearsAfterServerUpgradeOrConnectionChange() {
        QCoreApplication::setApplicationVersion("2.0.0");
        HttpFixture fixture; QVERIFY(fixture.listen(QHostAddress::LocalHost));
        AppInfo info;
        info.setBackend(fixture.url(), {}, QSslCertificate(), true); info.refreshServer();
        QTRY_VERIFY(!info.checkingServer());
        QVERIFY(!info.serverUpdateNotice().isEmpty());
        fixture.body = R"({"status":"ok","version":"2.0.0"})";
        info.refreshServer(); QTRY_VERIFY(!info.checkingServer());
        QVERIFY(info.serverUpdateNotice().isEmpty());
        info.setBackend("ssh://another-host", {}, QSslCertificate(), true);
        QVERIFY(info.serverUpdateNotice().isEmpty());
        QCoreApplication::setApplicationVersion("0.1.0");
    }
};
QTEST_GUILESS_MAIN(AppInfoTest)
#include "test_appinfo.moc"
