#include "usagefixture.h"
#include "scripted_backend.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QTemporaryDir>

using TestUsage::disabledCredentials;

class RefreshTransportTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void unsupportedRefreshFallsBackToCacheRead_data() {
        QTest::addColumn<int>("status");
        QTest::addColumn<QByteArray>("body");
        QTest::addColumn<QString>("kind");
        QTest::newRow("legacy-404") << 404 << QByteArray("not found") << "unsupported";
        QTest::newRow("legacy-405") << 405 << QByteArray("{\"error\":\"method not allowed\"}") << "unsupported";
        QTest::newRow("unavailable") << 503 << QByteArray("{\"error\":\"refresh unavailable\"}") << "unavailable";
        QTest::newRow("unexpected-200") << 200 << QByteArray("[]") << "unexpected";
        QTest::newRow("unexpected-500") << 500 << QByteArray("") << "unexpected";
    }
    void unsupportedRefreshFallsBackToCacheRead() {
        QFETCH(int, status); QFETCH(QByteArray, body); QFETCH(QString, kind);
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        backend.respond = [&](const QByteArray &request) {
            if (request.startsWith("POST /api/v1/usage/refresh ")) return httpResponse(status, body);
            return httpResponse(200, TestUsage::snapshot());
        };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        const int readsBefore = backend.count("GET /api/v1/usage ");
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QTRY_COMPARE(backend.count("GET /api/v1/usage "), readsBefore + 1);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        // The server stays connected: an unsupported or declined refresh is a notice, never an outage.
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.refreshStatus()["kind"].toString(), kind);
        QVERIFY(!controller.refreshStatus()["notice"].toString().isEmpty());
        QCOMPARE(controller.refreshStatus()["retrySeconds"].toInt(), 0);
        QCOMPARE(controller.providers().size(), 4);
        QTest::qWait(400);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1); // Never retried on its own.
        QCOMPARE(backend.count("GET /api/v1/usage "), readsBefore + 1);
        QVERIFY(!controller.diagnosticText().contains("127.0.0.1"));
    }
    void refreshFailuresShareUsageTransportHandling() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        int postStatus = 401; bool reachable = true;
        backend.respond = [&](const QByteArray &request) {
            if (request.startsWith("POST /api/v1/usage/refresh ")) return reachable ? httpResponse(postStatus, "{}") : QByteArray();
            return httpResponse(200, TestUsage::snapshot());
        };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("offline"));
        QCOMPARE(controller.state()["errorKind"].toString(), QString("auth"));
        QVERIFY(controller.refreshStatus()["notice"].toString().isEmpty());
        QCOMPARE(controller.providers().size(), 4);
        controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        reachable = false;
        controller.requestRefresh();
        QTRY_VERIFY(backend.count("POST /api/v1/usage/refresh ") >= 2);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("offline"));
        QCOMPARE(controller.state()["errorKind"].toString(), QString("network"));
        QVERIFY(controller.state()["retrySeconds"].toInt() <= 5);
        QVERIFY(controller.displayProviders().first().toMap()["stale"].toBool());
        QVERIFY(!controller.providers().first().toMap()["is_success"].toBool());
    }
    void sshRefreshUsesTheSelectedTransportAndHandlesLegacyReceivers() {
        QTemporaryDir dir; const QString path = dir.filePath("settings.json");
        SettingsService settings(path, false);
        auto saved = settings.value(); saved.connectionMode = "ssh"; saved.sshUrl = "ssh://valid";
        QVERIFY(settings.save(saved, true).isEmpty());
        Controller controller(path, nullptr, false, {}, disabledCredentials(), SshOptions{QStringLiteral(SSH_FIXTURE_PATH), 2000});
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        controller.requestRefresh();
        QTRY_COMPARE(controller.refreshStatus()["kind"].toString(), QString("accepted"));
        QVERIFY(controller.refreshStatus()["retrySeconds"].toInt() > 0);
        QCOMPARE(controller.backendUrl(), QString("ssh://valid"));
        // The fixed receiver on an older server answers 400 "invalid request": read the cache and say so.
        QVERIFY(controller.saveSettings("ssh", "", "", 60, false, "Claude", false, "ssh://legacy").isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.refreshStatus()["retrySeconds"].toInt(), 0);
        controller.requestRefresh();
        QTRY_COMPARE(controller.refreshStatus()["kind"].toString(), QString("unsupported"));
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.backendUrl(), QString("ssh://legacy")); // Never falls back to HTTP.
    }
};
QTEST_GUILESS_MAIN(RefreshTransportTest)
#include "test_refreshtransport.moc"
