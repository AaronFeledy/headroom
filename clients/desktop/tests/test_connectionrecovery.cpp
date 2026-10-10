#include "usagefixture.h"
#include "scripted_backend.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QTemporaryDir>
#include <QProcess>
#include <QFile>

using TestUsage::disabledCredentials;

class ConnectionRecoveryTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void cleanup() {
        qunsetenv("HEADROOM_FIXTURE_MODE");
        qunsetenv("HEADROOM_FIXTURE_RECORD");
    }
    void ownedServerExitInvalidatesRawSnapshotBeforeRestart() {
        // DO NOT turn this into a reset test: the ChatGPT credit metadata is only read.
        QTemporaryDir dir; const auto record = dir.filePath("server-record");
        QTcpServer reservation; QVERIFY(reservation.listen(QHostAddress::LocalHost));
        const quint16 port = reservation.serverPort(); reservation.close();
        qputenv("HEADROOM_FIXTURE_MODE", "controller-restart");
        qputenv("HEADROOM_FIXTURE_RECORD", record.toUtf8());
        ManagedServerOptions options;
        options.localUrl = QUrl(QString("http://127.0.0.1:%1/").arg(port));
        options.executablePath = QStringLiteral(MANAGED_FIXTURE_PATH);
        options.probeTimeoutMs = 8000; options.readinessProbeTimeoutMs = 750;
        options.readinessIntervalMs = 50; options.readinessAttempts = 30;
        Controller controller(dir.filePath("settings.json"), nullptr, false, options, disabledCredentials());
        QVERIFY(controller.saveSettings("local", "", "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE_WITH_TIMEOUT(controller.state()["status"].toString(), QString("ready"), 15000);
        const auto named = [](const QVariantList &list, const QString &name) {
            for (const auto &value : list) if (value.toMap()["provider_name"].toString() == name) return value.toMap();
            return QVariantMap{};
        };
        QCOMPARE(controller.providers().size(), 2);
        QCOMPARE(named(controller.providers(), "Claude")["buckets"].toList().first().toMap()["utilization"].toDouble(), 12.0);
        QCOMPARE(named(controller.providers(), "Codex")["rate_limit_reset_credits"].toMap()["available_count"].toLongLong(), 2);
        QVERIFY(controller.resetAction()["enabled"].toBool()); // Read only; nothing is prepared or sent.
        QCOMPARE(controller.displayProviders(), controller.providers());
        const qint64 pid = controller.ownedServerProcessId(); QVERIFY(pid > 0);
        auto server = controller.findChild<ManagedServer *>(); QVERIFY(server);
        // Observe the raw and presentation models the moment the server identity
        // changes, before any reply from a replacement process can arrive.
        struct Observation { int entries; bool rawSuccess, liveBuckets, stale, credits, resetEnabled; };
        QList<Observation> observations;
        connect(server, &ManagedServer::connectionChanged, this, [&] {
            Observation seen{int(controller.providers().size()), false, false, false, false, controller.resetAction()["enabled"].toBool()};
            for (const auto &value : controller.providers()) {
                const auto provider = value.toMap();
                seen.rawSuccess = seen.rawSuccess || provider["is_success"].toBool();
                seen.credits = seen.credits || !provider["rate_limit_reset_credits"].isNull();
            }
            for (const auto &value : controller.displayProviders()) {
                const auto provider = value.toMap();
                seen.liveBuckets = seen.liveBuckets || !provider["buckets"].toList().isEmpty();
                seen.stale = seen.stale || provider.contains("stale");
            }
            observations.append(seen);
        });
        QProcess *child = nullptr;
        for (auto process : controller.findChildren<QProcess *>()) if (process->processId() == pid) child = process;
        QVERIFY(child);
        child->kill();
        QTRY_VERIFY_WITH_TIMEOUT(observations.size() >= 1, 10000);
        // Exit, then the replacement's new identity: neither may publish the old peer's data.
        QTRY_VERIFY_WITH_TIMEOUT(observations.size() >= 2, 15000);
        for (const auto &seen : observations) {
            QCOMPARE(seen.entries, 2);
            QVERIFY(!seen.rawSuccess); QVERIFY(!seen.liveBuckets); QVERIFY(!seen.stale);
            QVERIFY(!seen.credits); QVERIFY(!seen.resetEnabled);
        }
        // Only the replacement's accepted snapshot restores live meters and credits.
        QTRY_COMPARE_WITH_TIMEOUT(controller.state()["status"].toString(), QString("ready"), 15000);
        QTRY_VERIFY(named(controller.providers(), "Claude")["is_success"].toBool());
        QCOMPARE(named(controller.providers(), "Claude")["buckets"].toList().first().toMap()["utilization"].toDouble(), 12.0);
        QCOMPARE(named(controller.providers(), "Codex")["rate_limit_reset_credits"].toMap()["available_count"].toLongLong(), 2);
        QCOMPARE(controller.displayProviders(), controller.providers());
        QVERIFY(controller.ownedServerProcessId() > 0); QVERIFY(controller.ownedServerProcessId() != pid);
        QFile file(record); QVERIFY(file.open(QIODevice::ReadOnly)); QCOMPARE(file.readAll().count("start\n"), 2);
        qunsetenv("HEADROOM_FIXTURE_MODE"); qunsetenv("HEADROOM_FIXTURE_RECORD");
    }
    void networkFailuresRetrySoonerThanTokenFailures() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        int status = 200; bool reachable = true;
        QByteArray body = TestUsage::snapshot();
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(status, status == 200 ? body : QByteArray("{}")) : QByteArray(); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        // A dead host is retried on the short network schedule: 5, 10, 20 seconds...
        reachable = false;
        for (const int seconds : {5, 10, 20}) {
            controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
            QCOMPARE(controller.state()["status"].toString(), QString("offline"));
            QCOMPARE(controller.state()["errorKind"].toString(), QString("network"));
            const int retry = controller.state()["retrySeconds"].toInt();
            QVERIFY2(retry > seconds - 3 && retry <= seconds, qPrintable(QString("attempt %1: retry in %2s").arg(seconds).arg(retry)));
        }
        // ...while a rejected token keeps the slower interval-based backoff.
        reachable = true; status = 401;
        controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["errorKind"].toString(), QString("auth"));
        QVERIFY(controller.state()["retrySeconds"].toInt() > 115);
        // A reachable server whose providers all failed is not a backend outage.
        status = 200;
        body = TestUsage::snapshotWithFetchStatus(QDateTime::currentDateTimeUtc().toString(Qt::ISODateWithMs), "epoch-a",
            {{"Claude", "other"}, {"Codex", "other"}, {"Cursor", "other"}, {"Grok", "other"}});
        controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.state()["retryAttempt"].toInt(), 0);
        QVERIFY(controller.state()["errorKind"].toString().isEmpty());
    }
    void backendOutageMarksRawProvidersUnavailable() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        bool reachable = true;
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(200, TestUsage::snapshot()) : QByteArray(); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.providers().size(), 4);
        reachable = false; controller.refresh();
        QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("offline"));
        // The raw model behind IPC, the tray, and notifications never passes old
        // success off as current while the server is unreachable.
        QCOMPARE(controller.providers().size(), 4);
        for (const auto &value : controller.providers()) {
            const auto provider = value.toMap();
            QVERIFY2(!provider["is_success"].toBool(), qPrintable(provider["provider_name"].toString()));
            QCOMPARE(provider["error"].toString(), QString("The usage server could not be reached."));
            QVERIFY(provider["buckets"].toList().isEmpty());
            QVERIFY(provider["rate_limit_reset_credits"].isNull());
            QVERIFY(!provider["needs_reauth"].toBool());
        }
        reachable = true; controller.refresh();
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        for (const auto &value : controller.providers()) QVERIFY(value.toMap()["is_success"].toBool());
    }
    void transientProviderFailureShortensCacheReads() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        const auto stamp = [](int offset) { return QDateTime::currentDateTimeUtc().addSecs(offset).toString(Qt::ISODateWithMs); };
        QByteArray body = TestUsage::snapshotWithFetchStatus(stamp(0), "epoch-a", {{"Claude", "transient"}});
        backend.respond = [&](const QByteArray &) { return httpResponse(200, body); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        // The server retries a transient provider failure itself; read its cache sooner, but never faster than 15 seconds.
        int retry = controller.state()["retrySeconds"].toInt();
        QVERIFY2(retry > 0 && retry <= 15, qPrintable(QString::number(retry)));
        body = TestUsage::snapshotWithFetchStatus(stamp(1), "epoch-a");
        controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        retry = controller.state()["retrySeconds"].toInt();
        QVERIFY2(retry > 15, qPrintable(QString::number(retry)));
        // Rate-limited and sign-in failures do not justify a higher cadence.
        body = TestUsage::snapshotWithFetchStatus(stamp(2), "epoch-a", {{"Claude", "rate_limited"}, {"Codex", "auth"}}, {{"Codex", "expired"}});
        controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        QVERIFY(controller.state()["retrySeconds"].toInt() > 15);
    }
    void everyBackendFailureMasksRawProviders() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        int status = 200; bool reachable = true; QByteArray body = TestUsage::snapshot();
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(status, status == 200 ? body : QByteArray("{}")) : QByteArray(); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "synthetic-bearer", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        auto masked = [&](const QString &kind) {
            QTRY_VERIFY(!controller.state()["loading"].toBool());
            QCOMPARE(controller.state()["status"].toString(), QString("offline"));
            QCOMPARE(controller.state()["errorKind"].toString(), kind);
            QCOMPARE(controller.providers().size(), 4);
            for (const auto &value : controller.providers()) {
                const auto provider = value.toMap();
                QVERIFY2(!provider["is_success"].toBool(), qPrintable(kind + ": " + provider["provider_name"].toString()));
                QVERIFY(provider["buckets"].toList().isEmpty());
                QVERIFY(provider["rate_limit_reset_credits"].isNull());
                QVERIFY(!provider["needs_reauth"].toBool());
                const QString error = provider["error"].toString();
                QVERIFY(!error.isEmpty()); QVERIFY(!error.contains("synthetic-bearer")); QVERIFY(!error.contains("127.0.0.1"));
            }
        };
        auto stale = [&] {
            for (const auto &value : controller.displayProviders()) if (value.toMap()["stale"].toBool()) return true;
            return false;
        };
        // A rejected token, a server error, and a malformed body are not outages: the raw
        // model stops claiming success at once and the dashboard shows nothing stale.
        status = 401; controller.refresh(); masked("auth");
        QVERIFY(!stale()); QCOMPARE(controller.displayProviders(), controller.providers());
        status = 200; controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        for (const auto &value : controller.providers()) QVERIFY(value.toMap()["is_success"].toBool());
        status = 503; controller.refresh(); masked("api"); QVERIFY(!stale());
        status = 200; controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        body = "[{}]"; controller.refresh(); masked("malformed"); QVERIFY(!stale());
        body = TestUsage::snapshot(); controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        // Only an unreachable server keeps the reading on the dashboard, and a token
        // rejection during that outage retires it instead of leaving it on screen.
        reachable = false; controller.refresh(); masked("network"); QVERIFY(stale());
        reachable = true; status = 401; controller.refresh(); masked("auth"); QVERIFY(!stale());
        reachable = false; controller.refresh(); masked("network"); QVERIFY(!stale());
        reachable = true; status = 200; controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.displayProviders(), controller.providers());
    }
};
QTEST_GUILESS_MAIN(ConnectionRecoveryTest)
#include "test_connectionrecovery.moc"
