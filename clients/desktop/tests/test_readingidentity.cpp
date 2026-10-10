#include "usagefixture.h"
#include "scripted_backend.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QTemporaryDir>

using TestUsage::disabledCredentials;

class ReadingIdentityTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void backendOutageKeepsDisplayReadingsUntilTokenRejectionOrRecovery() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        int status = 200; bool reachable = true;
        // A legacy snapshot without fetch metadata still qualifies: the outage rule is about this connection, not epochs.
        QByteArray body = TestUsage::snapshot();
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(status, status == 200 ? body : QByteArray("{}")) : QByteArray(); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        const auto live = controller.providers();
        reachable = false; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("offline"));
        QCOMPARE(controller.displayProviders().size(), 4);
        for (int i = 0; i < 4; ++i) {
            const auto shown = controller.displayProviders()[i].toMap();
            QVERIFY2(shown["stale"].toBool(), qPrintable(shown["provider_name"].toString()));
            QVERIFY(!shown["is_success"].toBool());
            QCOMPARE(shown["buckets"].toList(), live[i].toMap()["buckets"].toList());
            QCOMPARE(shown["error"].toString(), QString("The usage server could not be reached."));
            QVERIFY(shown["rate_limit_reset_credits"].isNull());
        }
        // Repeated failures keep the same reading and age; they never refresh it.
        const qint64 observed = controller.displayProviders()[0].toMap()["last_reading_at"].toLongLong();
        QTest::qWait(1100); controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.displayProviders()[0].toMap()["last_reading_at"].toLongLong(), observed);
        // A rejected token means the peer may not be the same server: drop the readings.
        reachable = true; status = 401; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["errorKind"].toString(), QString("auth"));
        for (const auto &value : controller.displayProviders()) {
            QVERIFY(!value.toMap().contains("stale")); QVERIFY(!value.toMap()["is_success"].toBool());
        }
        status = 200; controller.refresh(); QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        for (const auto &value : controller.displayProviders()) {
            QVERIFY(!value.toMap().contains("stale")); QVERIFY(value.toMap()["is_success"].toBool());
        }
        QCOMPARE(controller.displayProviders(), controller.providers());
    }
    void connectionChangeDropsRetainedReadings() {
        QTemporaryDir dir; ScriptedBackend first, second; QVERIFY(first.listen()); QVERIFY(second.listen());
        const QString stamp = QDateTime::currentDateTimeUtc().toString(Qt::ISODateWithMs);
        QByteArray body = TestUsage::snapshotWithFetchStatus(stamp, "epoch-a");
        first.respond = [&](const QByteArray &) { return httpResponse(200, body); };
        // The other server reports the very same epoch string; it is still a different peer.
        second.respond = [&](const QByteArray &) { return httpResponse(200, TestUsage::snapshotWithFetchStatus(stamp, "epoch-a", {{"Claude", "transient"}})); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", first.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        body = TestUsage::snapshotWithFetchStatus(stamp, "epoch-a", {{"Claude", "transient"}});
        controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QVERIFY(controller.displayProviders().first().toMap()["stale"].toBool());
        QVERIFY(controller.saveSettings("remote", second.url(), "", 60, false, "Claude", false).isEmpty());
        QVERIFY(controller.displayProviders().isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QCOMPARE(controller.displayProviders().size(), 4);
        QVERIFY(!controller.displayProviders().first().toMap().contains("stale"));
        QVERIFY(controller.displayProviders().first().toMap()["buckets"].toList().isEmpty());
    }
    void staleReadingAgeComesFromServerFetchTime() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        bool reachable = true; QByteArray body;
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(200, body) : QByteArray(); };
        const auto stamp = [](qint64 secs) { return QDateTime::fromSecsSinceEpoch(secs, QTimeZone::UTC).toString(Qt::ISODateWithMs); };
        const qint64 fetched = QDateTime::currentSecsSinceEpoch() - 300;
        // The server's cache already held a five-minute-old reading when this session first read it.
        body = TestUsage::snapshotWithFetchStatus(stamp(fetched), "epoch-a");
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        auto read = [&](const QByteArray &payload) { body = payload; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool()); };
        auto claude = [&] { return controller.displayProviders().first().toMap(); };
        read(TestUsage::snapshotWithFetchStatus(stamp(fetched + 300), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY(claude()["stale"].toBool());
        const qint64 shown = claude()["last_reading_at"].toLongLong();
        QVERIFY2(qAbs(shown - fetched) <= 1, qPrintable(QString("last_reading_at %1 vs server fetched_at %2").arg(shown).arg(fetched)));
        QVERIFY(controller.readingAgeLabel(shown).startsWith("5m ago"));
        // Rereading the identical cached success later never makes the reading younger.
        QTest::qWait(1100);
        read(TestUsage::snapshotWithFetchStatus(stamp(fetched), "epoch-a"));
        read(TestUsage::snapshotWithFetchStatus(stamp(fetched + 301), "epoch-a", {{"Claude", "transient"}}));
        QCOMPARE(claude()["last_reading_at"].toLongLong(), shown);
        // A server clock running ahead cannot make a reading look newer than now.
        read(TestUsage::snapshotWithFetchStatus(stamp(fetched + 3900), "epoch-a"));
        const qint64 before = QDateTime::currentSecsSinceEpoch();
        read(TestUsage::snapshotWithFetchStatus(stamp(fetched + 3901), "epoch-a", {{"Claude", "transient"}}));
        const qint64 clamped = claude()["last_reading_at"].toLongLong();
        QVERIFY2(clamped >= before - 1 && clamped <= QDateTime::currentSecsSinceEpoch(), qPrintable(QString::number(clamped)));
        // Without fetch metadata the desktop's own observation time is the only stand-in.
        const qint64 legacyBefore = QDateTime::currentSecsSinceEpoch();
        read(TestUsage::snapshot());
        reachable = false; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool());
        QCOMPARE(controller.state()["status"].toString(), QString("offline"));
        const qint64 legacy = claude()["last_reading_at"].toLongLong();
        QVERIFY2(legacy >= legacyBefore && legacy <= QDateTime::currentSecsSinceEpoch(), qPrintable(QString::number(legacy)));
    }
    void contextChangeErasesRetainedReading() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        QByteArray body;
        backend.respond = [&](const QByteArray &) { return httpResponse(200, body); };
        const auto base = QDateTime::currentDateTimeUtc().addSecs(-60);
        const auto stamp = [&](int offset) { return base.addSecs(offset).toString(Qt::ISODateWithMs); };
        auto claudeUsage = [](QByteArray payload, double used, bool metadata) {
            auto providers = QJsonDocument::fromJson(payload).array();
            auto claude = providers[0].toObject();
            if (!metadata) claude.remove("fetch_status");
            if (used >= 0) {
                auto buckets = claude["buckets"].toArray(); auto session = buckets[0].toObject();
                session["utilization"] = used; buckets[0] = session; claude["buckets"] = buckets;
            }
            providers[0] = claude;
            return QJsonDocument(providers).toJson(QJsonDocument::Compact);
        };
        body = TestUsage::snapshotWithFetchStatus(stamp(0), "epoch-a");
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        auto read = [&](const QByteArray &payload) { body = payload; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool()); };
        auto claude = [&] { return controller.displayProviders().first().toMap(); };
        // A failure under another credential epoch retires the reading for good: the
        // old epoch returning must not resurrect the earlier context's numbers.
        read(TestUsage::snapshotWithFetchStatus(stamp(1), "epoch-b", {{"Claude", "transient"}}));
        QVERIFY(!claude().contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(2), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY2(!claude().contains("stale"), "epoch-a reading resurrected after an epoch-b failure");
        // A failure that lost the metadata the reading was keyed on retires it too.
        read(TestUsage::snapshotWithFetchStatus(stamp(3), "epoch-a"));
        read(claudeUsage(TestUsage::snapshotWithFetchStatus(stamp(4), "epoch-a", {{"Claude", "transient"}}), -1, false));
        QVERIFY(!claude().contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(5), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY2(!claude().contains("stale"), "reading resurrected after a failure without fetch metadata");
        // A success in a new context replaces the buckets even when its attempt time reads the same.
        read(claudeUsage(TestUsage::snapshotWithFetchStatus(stamp(6), "epoch-a"), 34, true));
        read(claudeUsage(TestUsage::snapshotWithFetchStatus(stamp(6), "epoch-b"), 77, true));
        read(TestUsage::snapshotWithFetchStatus(stamp(7), "epoch-b", {{"Claude", "transient"}}));
        QVERIFY(claude()["stale"].toBool());
        QCOMPARE(claude()["buckets"].toList().first().toMap()["utilization"].toDouble(), 77.0);
        // Same context, same attempt, same buckets: still one reading.
        read(claudeUsage(TestUsage::snapshotWithFetchStatus(stamp(8), "epoch-b"), 77, true));
        const qint64 observed = [&] { read(TestUsage::snapshotWithFetchStatus(stamp(9), "epoch-b", {{"Claude", "transient"}})); return claude()["last_reading_at"].toLongLong(); }();
        QTest::qWait(1100);
        read(claudeUsage(TestUsage::snapshotWithFetchStatus(stamp(8), "epoch-b"), 77, true));
        read(TestUsage::snapshotWithFetchStatus(stamp(10), "epoch-b", {{"Claude", "transient"}}));
        QCOMPARE(claude()["last_reading_at"].toLongLong(), observed);
    }
};
QTEST_GUILESS_MAIN(ReadingIdentityTest)
#include "test_readingidentity.moc"
