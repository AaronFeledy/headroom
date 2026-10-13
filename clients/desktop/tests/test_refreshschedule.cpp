#include "usagefixture.h"
#include "scripted_backend.h"
#include "tls_fixture.h"
#include "http_assertions.h"
#include <QtTest>
#include <QTemporaryDir>

using TestUsage::disabledCredentials;

class RefreshScheduleTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void explicitRefreshPostsOnceThenReadsCacheOnSchedule() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        const auto base = QDateTime::currentDateTimeUtc();
        QString fetchedAt = base.toString(Qt::ISODateWithMs);
        QByteArray accepted = R"({"status":"accepted","retry_after_seconds":15})";
        backend.respond = [&](const QByteArray &request) {
            if (request.startsWith("POST /api/v1/usage/refresh ")) return httpResponse(202, accepted);
            return httpResponse(200, TestUsage::snapshotWithFetchStatus(fetchedAt, "epoch-a"));
        };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.refreshStatus()["retrySeconds"].toInt(), 0);
        QVERIFY(controller.refreshStatus()["notice"].toString().isEmpty());
        const qint64 lastGood = controller.state()["lastGood"].toLongLong();
        QTest::qWait(1100);
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        const QByteArray post = backend.requests.last();
        QVERIFY2(post.startsWith("POST /api/v1/usage/refresh HTTP/1.1\r\n"), post.constData()); // Same prefix, no query.
        QVERIFY(HttpAssertions::hasHeader(post, "Content-Length", "0"));
        // Acceptance is not a fresh reading: the snapshot age is untouched and the action waits out the server's window.
        QCOMPARE(controller.state()["lastGood"].toLongLong(), lastGood);
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        auto status = controller.refreshStatus();
        QCOMPARE(status["kind"].toString(), QString("accepted"));
        QVERIFY(status["active"].toBool());
        QVERIFY2(status["retrySeconds"].toInt() >= 13 && status["retrySeconds"].toInt() <= 15, qPrintable(status["retrySeconds"].toString()));
        QVERIFY(status["notice"].toString().contains("Refresh requested"));
        QVERIFY(controller.diagnosticText().contains("Provider refresh accepted"));
        controller.requestRefresh();
        QTest::qWait(50);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        // Cache reads follow at 2 and 5 seconds. The first still sees the old attempt time;
        // the second sees every provider refetched and ends the window early.
        const int readsBefore = backend.count("GET /api/v1/usage ");
        QTRY_VERIFY_WITH_TIMEOUT(backend.count("GET /api/v1/usage ") == readsBefore + 1, 3500);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QVERIFY(controller.refreshStatus()["active"].toBool());
        fetchedAt = base.addSecs(3).toString(Qt::ISODateWithMs);
        QTRY_VERIFY_WITH_TIMEOUT(backend.count("GET /api/v1/usage ") == readsBefore + 2, 4000);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QTRY_VERIFY(!controller.refreshStatus()["active"].toBool());
        QVERIFY(controller.refreshStatus()["notice"].toString().isEmpty());
        QTest::qWait(400);
        QCOMPARE(backend.count("GET /api/v1/usage "), readsBefore + 2);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QVERIFY(controller.refreshStatus()["retrySeconds"].toInt() > 0);
        controller.requestRefresh(); QTest::qWait(50);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        // Saving the same connection keeps the server's window; a changed connection starts over.
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 30, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QVERIFY(controller.refreshStatus()["retrySeconds"].toInt() > 0);
        // A coalesced acceptance behaves the same way and is reported as such.
        accepted = R"({"status":"coalesced","retry_after_seconds":1})";
        QVERIFY(controller.saveSettings("remote", backend.url() + "/", "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.refreshStatus()["retrySeconds"].toInt(), 0);
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 2);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.refreshStatus()["kind"].toString(), QString("coalesced"));
        QVERIFY(controller.refreshStatus()["notice"].toString().contains("already underway"));
        QTRY_VERIFY_WITH_TIMEOUT(controller.refreshStatus()["retrySeconds"].toInt() == 0, 2500);
    }
    void rateLimitedRefreshReadsCacheOnceAndWaits() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        const QString fetchedAt = QDateTime::currentDateTimeUtc().toString(Qt::ISODateWithMs);
        backend.respond = [&](const QByteArray &request) {
            // The JSON body is authoritative; an SSH receiver has no headers to offer, so the header is ignored.
            if (request.startsWith("POST /api/v1/usage/refresh "))
                return httpResponse(429, R"({"error":"refresh rate limited","retry_after_seconds":2})", "Retry-After: 99\r\n");
            return httpResponse(200, TestUsage::snapshotWithFetchStatus(fetchedAt, "epoch-a"));
        };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        const int readsBefore = backend.count("GET /api/v1/usage ");
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QTRY_COMPARE(backend.count("GET /api/v1/usage "), readsBefore + 1);
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        auto status = controller.refreshStatus();
        QCOMPARE(status["kind"].toString(), QString("limited"));
        QVERIFY(!status["active"].toBool());
        QVERIFY2(status["retrySeconds"].toInt() >= 1 && status["retrySeconds"].toInt() <= 2, qPrintable(status["retrySeconds"].toString()));
        QVERIFY(status["notice"].toString().contains("Refresh limited"));
        controller.requestRefresh(); QTest::qWait(300);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QCOMPARE(backend.count("GET /api/v1/usage "), readsBefore + 1); // One cache read, no follow-up schedule.
        QTRY_VERIFY_WITH_TIMEOUT(controller.refreshStatus()["retrySeconds"].toInt() == 0, 3000);
        QVERIFY(controller.refreshStatus()["notice"].toString().isEmpty());
        controller.requestRefresh();
        QTRY_COMPARE(backend.count("POST /api/v1/usage/refresh "), 2);
    }
    void rateLimitedRefreshHonorsLongServerCooldowns_data() {
        QTest::addColumn<QByteArray>("body");
        QTest::addColumn<int>("minimum");
        QTest::addColumn<int>("maximum");
        // A long all-provider cooldown is honored as reported, never shortened to a default.
        QTest::newRow("hour") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":3600})") << 3598 << 3600;
        QTest::newRow("day") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":86400})") << 86398 << 86400;
        QTest::newRow("int-max") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":2147483647})") << 2147483645 << 2147483647;
        // Unusable delays fall back to the usual short wait rather than blocking Refresh indefinitely.
        QTest::newRow("zero") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":0})") << 13 << 15;
        QTest::newRow("negative") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":-30})") << 13 << 15;
        QTest::newRow("fraction") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":2.5})") << 13 << 15;
        QTest::newRow("beyond-int") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":2147483648})") << 13 << 15;
        QTest::newRow("huge") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":1e300})") << 13 << 15;
        QTest::newRow("string") << QByteArray(R"({"error":"refresh rate limited","retry_after_seconds":"3600"})") << 13 << 15;
        QTest::newRow("missing") << QByteArray(R"({"error":"refresh rate limited"})") << 13 << 15;
    }
    void rateLimitedRefreshHonorsLongServerCooldowns() {
        QFETCH(QByteArray, body); QFETCH(int, minimum); QFETCH(int, maximum);
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        backend.respond = [&](const QByteArray &request) {
            if (request.startsWith("POST /api/v1/usage/refresh ")) return httpResponse(429, body);
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
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        const auto status = controller.refreshStatus();
        QCOMPARE(status["kind"].toString(), QString("limited"));
        const int retry = status["retrySeconds"].toInt();
        QVERIFY2(retry >= minimum && retry <= maximum, qPrintable(QString("retrySeconds %1, expected %2..%3").arg(retry).arg(minimum).arg(maximum)));
        QVERIFY(status["notice"].toString().contains(QString("try again in %1s").arg(retry)));
        // The action stays blocked for the reported cooldown: no second POST and no extra cache read.
        controller.requestRefresh(); QTest::qWait(200);
        QCOMPARE(backend.count("POST /api/v1/usage/refresh "), 1);
        QCOMPARE(backend.count("GET /api/v1/usage "), readsBefore + 1);
        QVERIFY(controller.refreshStatus()["retrySeconds"].toInt() >= minimum - 1);
    }
};
QTEST_GUILESS_MAIN(RefreshScheduleTest)
#include "test_refreshschedule.moc"
