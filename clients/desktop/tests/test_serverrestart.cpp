#include "managedserver.h"
#include "usagefixture.h"
#include "tls_fixture.h"
#include "statemachine_assertions.h"

#include <QFile>
#include <QSignalSpy>
#include <QTcpServer>
#include <QTcpSocket>
#include <QTemporaryDir>
#include <QtTest>

// Answers the local health probe so ManagedServer attaches without owning it.
class AttachedServer : public QTcpServer {
public:
    AttachedServer() {
        connect(this, &QTcpServer::newConnection, this, [this] {
            while (auto socket = nextPendingConnection()) {
                connect(socket, &QTcpSocket::readyRead, socket, [socket] {
                    const QByteArray request = socket->property("request").toByteArray() + socket->readAll();
                    socket->setProperty("request", request);
                    if (!request.contains("\r\n\r\n")) return;
                    const QByteArray body = R"({"status":"ok","version":"dev","providers":[]})";
                    socket->write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
                        + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                    socket->disconnectFromHost();
                });
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
            }
        });
    }
};

class ServerRestartTest : public QObject {
    Q_OBJECT
private:
    QJsonObject request(const QString &path) const {
        return {{"schema", 1}, {"product", "Headroom"}, {"command", "restart-server"}, {"config_path", path}};
    }
    ManagedServerOptions options() const {
        QTcpServer reservation;
        if (!reservation.listen(QHostAddress::LocalHost)) qFatal("Cannot reserve fixture port");
        ManagedServerOptions value;
        value.localUrl = QUrl(QString("http://127.0.0.1:%1/").arg(reservation.serverPort()));
        value.executablePath = QStringLiteral(FIXTURE_PATH);
        value.restartLimit = 0;
        value.sessionHandshakeTimeoutMs = 1000;
        return value;
    }
private slots:
    void initTestCase() { QVERIFY(TlsFixture::selectNativeTestBackend()); }
    void init() { failOnStateMachineWarning(); }
    void cleanup() {
        qunsetenv("HEADROOM_FIXTURE_CONFIG_PATH");
        qunsetenv("HEADROOM_FIXTURE_MODE");
    }
    void declinesWithoutOwnership_data() {
        QTest::addColumn<QString>("mode");
        QTest::addColumn<bool>("busy");
        QTest::addColumn<QString>("result");
        QTest::newRow("remote") << "remote" << false << "not_owner";
        QTest::newRow("local-unowned") << "local" << false << "not_owner";
        QTest::newRow("update-busy") << "local" << true << "busy";
    }
    void declinesWithoutOwnership() {
        QFETCH(QString, mode); QFETCH(bool, busy); QFETCH(QString, result);
        ManagedServer server(options()); server.configure(mode, {});
        QJsonObject response;
        server.requestRestart(request(QDir::tempPath() + "/config.yaml"), busy, [&](const auto &value) { response = value; });
        QCOMPARE(response.value("result").toString(), result);
        QVERIFY(response.value("ok").toBool()); QVERIFY(!server.ownsProcess());
    }
    void rejectsInvalidRequest() {
        ManagedServer server(options()); QJsonObject response;
        server.requestRestart(request("relative.yaml"), false, [&](const auto &value) { response = value; });
        QCOMPARE(response.value("ok").toBool(), false);
        QVERIFY(!response.value("error").toString().isEmpty());
    }
    void controllerReadsReplacementSnapshot() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath("config.yaml");
        QFile config(path); QVERIFY(config.open(QIODevice::WriteOnly)); config.close();
        qputenv("HEADROOM_FIXTURE_CONFIG_PATH", path.toUtf8());
        Controller controller(dir.filePath("settings.json"), nullptr, false, options(), TestUsage::disabledCredentials());
        QVERIFY(controller.saveSettings("local", "", "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE_WITH_TIMEOUT(controller.state()["status"].toString(), QString("ready"), 15000);
        QCOMPARE(controller.providers().size(), 0);
        const auto originalPid = controller.ownedServerProcessId();
        qputenv("HEADROOM_FIXTURE_MODE", "controller-recovery");
        QJsonObject response;
        controller.requestServerRestart(request(path), false, [&](const auto &value) { response = value; });
        QTRY_COMPARE_WITH_TIMEOUT(response.value("result").toString(), QString("restarted"), 15000);
        QTRY_COMPARE_WITH_TIMEOUT(controller.providers().size(), 2, 15000);
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        QVERIFY(controller.ownedServerProcessId() != originalPid);
    }
    void attachedServerIsNeverRestarted() {
        AttachedServer attached; QVERIFY(attached.listen(QHostAddress::LocalHost));
        auto configured = options();
        configured.localUrl = QUrl(QString("http://127.0.0.1:%1/").arg(attached.serverPort()));
        ManagedServer server(configured); QSignalSpy ready(&server, &ManagedServer::available);
        server.configure("local", {}); server.ensureAvailable();
        QTRY_COMPARE_WITH_TIMEOUT(ready.size(), 1, 15000);
        QVERIFY(server.isAttached()); QVERIFY(!server.ownsProcess());
        QJsonObject response;
        server.requestRestart(request(QDir::tempPath() + "/config.yaml"), false, [&](const auto &value) { response = value; });
        QCOMPARE(response.value("result").toString(), QString("not_owner"));
        QVERIFY(attached.isListening()); QVERIFY(server.isAttached());
    }
    void ownedRestart_data() {
        QTest::addColumn<QString>("scenario");
        for (const auto &name : {"restart", "canonical", "other", "unknown", "failed", "changed-config", "update-busy", "timeout"})
            QTest::newRow(name) << QString(name);
    }
    void ownedRestart() {
        QFETCH(QString, scenario);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath("config.yaml");
        QFile config(path); QVERIFY(config.open(QIODevice::WriteOnly)); config.close();
        if (scenario != "unknown") qputenv("HEADROOM_FIXTURE_CONFIG_PATH", path.toUtf8());
        auto configured = options();
        if (scenario == "timeout") {
            configured.sessionHandshakeTimeoutMs = 90000;
            configured.restartTimeoutMs = 500;
        }
        ManagedServer server(configured); QSignalSpy ready(&server, &ManagedServer::available);
        server.configure("local", {}); server.ensureAvailable();
        QTRY_COMPARE_WITH_TIMEOUT(ready.size(), 1, 15000);
        const auto originalPid = server.ownedProcessId(); QVERIFY(originalPid > 0);
        QString target = scenario == "other" ? dir.filePath("other.yaml") : path;
        if (scenario == "canonical") {
            target = dir.filePath("alias.yaml"); QVERIFY(QFile::link(path, target));
#ifdef Q_OS_WIN
            // QFile::link creates a shortcut on Windows, not a filesystem symlink.
            target = dir.filePath("./config.yaml");
#endif
        }
        if (scenario == "failed") qputenv("HEADROOM_FIXTURE_MODE", "bad-nonce");
        if (scenario == "timeout") qputenv("HEADROOM_FIXTURE_MODE", "identity-hang");
        if (scenario == "changed-config") qputenv("HEADROOM_FIXTURE_CONFIG_PATH", dir.filePath("other.yaml").toUtf8());
        QJsonObject response;
        QSignalSpy failed(&server, &ManagedServer::unavailable);
        server.requestRestart(request(target), scenario == "update-busy", [&](const auto &value) { response = value; });
        if (scenario != "other" && scenario != "unknown" && scenario != "update-busy") {
            QJsonObject concurrent;
            server.requestRestart(request(path), false, [&](const auto &value) { concurrent = value; });
            QCOMPARE(concurrent.value("result").toString(), QString("busy"));
        }
        QTRY_VERIFY_WITH_TIMEOUT(!response.isEmpty(), 15000);
        if (scenario == "update-busy") {
            QCOMPARE(response.value("result").toString(), QString("busy"));
            QCOMPARE(server.ownedProcessId(), originalPid);
        } else if (scenario == "other" || scenario == "unknown") {
            QCOMPARE(response.value("result").toString(), QString("other_config"));
            QCOMPARE(response.value("config_path").toString(), scenario == "unknown" ? QString() : path);
            QCOMPARE(server.ownedProcessId(), originalPid);
        } else if (scenario == "failed" || scenario == "changed-config" || scenario == "timeout") {
            QCOMPARE(response.value("ok").toBool(), false);
            QVERIFY(!response.value("error").toString().isEmpty());
            if (scenario == "timeout") {
                QVERIFY(!server.ownsProcess());
                QCOMPARE(server.state(), QString("failed"));
                QCOMPARE(failed.size(), 1);
                QCOMPARE(failed.first().at(1).toString(), QString("timeout"));
            }
        } else {
            QVERIFY(response.value("ok").toBool());
            QCOMPARE(response.value("result").toString(), QString("restarted"));
            QCOMPARE(response.value("config_path").toString(), path);
            QVERIFY(server.ownedProcessId() > 0); QVERIFY(server.ownedProcessId() != originalPid);
        }
    }
};

QTEST_GUILESS_MAIN(ServerRestartTest)
#include "test_serverrestart.moc"
