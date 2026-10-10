#include "usagefixture.h"
#include "scripted_backend.h"
#include "tls_fixture.h"
#include <QtTest>
#include <QTemporaryDir>

using TestUsage::disabledCredentials;

class ProviderReadingsTest : public QObject {
    Q_OBJECT
private slots:
    void initTestCase() {
        QVERIFY2(TlsFixture::selectNativeTestBackend(), "SecureTransport is unavailable");
    }
    void fetchStatusMetadataIsOptionalAndSanitized() {
        auto provider = QJsonDocument::fromJson(TestUsage::snapshot()).array()[0].toObject();
        const QString stamp = "2026-10-09T10:00:00.123456789Z";
        auto normalized = [&](const QJsonValue &status, bool include = true) {
            if (include) provider["fetch_status"] = status; else provider.remove("fetch_status");
            QVariantList parsed;
            if (!Usage::parse(QJsonDocument(QJsonArray{provider}).toJson(), parsed)) return QVariant(QStringLiteral("rejected"));
            return parsed.first().toMap()["fetch_status"];
        };
        // Older servers omit the metadata; it never breaks parsing and never enables stale readings.
        QVERIFY(normalized({}, false).isNull());
        QVERIFY(normalized(QJsonValue(QJsonValue::Null)).isNull());
        QVERIFY(normalized(QJsonArray{1}).isNull());
        QVERIFY(normalized(QJsonValue("2026-10-09T10:00:00Z")).isNull());
        auto success = normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", "epoch-a"}}).toMap();
        QCOMPARE(success["fetched_at"].toString(), stamp);
        QVERIFY(success["failure_kind"].isNull());
        QCOMPARE(success["credential_epoch"].toString(), QString("epoch-a"));
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}}).toMap().contains("credential_epoch")); // Missing optional keys read as null.
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", ""}}).isNull());
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", 7}}).isNull());
        QVERIFY(normalized(QJsonObject{{"fetched_at", "yesterday"}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", "epoch-a"}}).isNull());
        QVERIFY(normalized(QJsonObject{{"fetched_at", 12}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", "epoch-a"}}).isNull());
        // A failure kind contradicting the frozen error contract is dropped rather than trusted.
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", "transient"}, {"credential_epoch", "epoch-a"}}).isNull());
        provider["error"] = "Request timed out"; provider["is_success"] = false; provider["buckets"] = QJsonArray{};
        for (const QString kind : {"transient", "rate_limited", "auth", "other"}) {
            const auto failed = normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", kind}, {"credential_epoch", "epoch-a"}}).toMap();
            QCOMPARE(failed["failure_kind"].toString(), kind);
        }
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", "mystery"}, {"credential_epoch", "epoch-a"}}).isNull());
        QVERIFY(normalized(QJsonObject{{"fetched_at", stamp}, {"failure_kind", QJsonValue::Null}, {"credential_epoch", "epoch-a"}}).isNull());
        QVariantList parsed;
        QVERIFY(Usage::parse(QJsonDocument(QJsonArray{provider}).toJson(), parsed));
        QVERIFY(parsed.first().toMap()["buckets"].toList().isEmpty());
        QVERIFY(!parsed.first().toMap()["is_success"].toBool());
    }
    void staleReadingsFollowProviderFailureKinds() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        const auto base = QDateTime::currentDateTimeUtc().addSecs(-120);
        const auto stamp = [&](int offset) { return base.addSecs(offset).toString(Qt::ISODateWithMs); };
        QByteArray body = TestUsage::snapshotWithFetchStatus(stamp(0), "epoch-a");
        backend.respond = [&](const QByteArray &) { return httpResponse(200, body); };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Claude", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        auto entry = [](const QVariantList &list, const QString &name) {
            for (const auto &value : list) if (value.toMap()["provider_name"].toString() == name) return value.toMap();
            return QVariantMap{};
        };
        auto shown = [&](const QString &name) { return entry(controller.displayProviders(), name); };
        auto raw = [&](const QString &name) { return entry(controller.providers(), name); };
        auto read = [&](const QByteArray &payload) { body = payload; controller.refresh(); QTRY_VERIFY(!controller.state()["loading"].toBool()); };
        QCOMPARE(controller.displayProviders().size(), 4);
        QVERIFY(!shown("Claude").contains("stale"));
        const auto liveBuckets = raw("Claude")["buckets"].toList(); QCOMPARE(liveBuckets.size(), 3);
        // Transient failure, same credential epoch, still signed in: the last reading stays visible, clearly stale.
        read(TestUsage::snapshotWithFetchStatus(stamp(1), "epoch-a", {{"Claude", "transient"}}));
        QCOMPARE(controller.state()["status"].toString(), QString("ready"));
        auto stale = shown("Claude");
        QVERIFY(stale["stale"].toBool()); QVERIFY(!stale["is_success"].toBool());
        QCOMPARE(stale["buckets"].toList(), liveBuckets);
        QCOMPARE(stale["error"].toString(), QString("Request timed out"));
        const qint64 observed = stale["last_reading_at"].toLongLong();
        QVERIFY(observed > 0 && observed <= QDateTime::currentSecsSinceEpoch());
        QVERIFY(!controller.readingAgeLabel(observed).isEmpty());
        // The raw model stays an honest failure for IPC, tray, and notifications.
        QVERIFY(!raw("Claude").contains("stale")); QVERIFY(raw("Claude")["buckets"].toList().isEmpty());
        QVERIFY(raw("Claude")["rate_limit_reset_credits"].isNull());
        // Re-reading the server's unchanged cache never advances the reading's age.
        QTest::qWait(1100);
        read(TestUsage::snapshotWithFetchStatus(stamp(0), "epoch-a"));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(2), "epoch-a", {{"Claude", "transient"}}));
        QCOMPARE(shown("Claude")["last_reading_at"].toLongLong(), observed);
        // Rate limiting may reuse the reading; an unclassified failure in the same
        // context keeps it without showing it; another epoch retires it for good.
        read(TestUsage::snapshotWithFetchStatus(stamp(3), "epoch-a", {{"Claude", "rate_limited"}}));
        QVERIFY(shown("Claude")["stale"].toBool());
        read(TestUsage::snapshotWithFetchStatus(stamp(4), "epoch-a", {{"Claude", "other"}}));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(5), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY(shown("Claude")["stale"].toBool());
        read(TestUsage::snapshotWithFetchStatus(stamp(6), "epoch-b", {{"Claude", "transient"}}));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(6), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY2(!shown("Claude").contains("stale"), "epoch-a reading resurrected after an epoch-b failure");
        // Source or sign-in changes discard the reading for good, even with the old epoch.
        read(TestUsage::snapshotWithFetchStatus(stamp(7), "epoch-a"));
        read(TestUsage::snapshotWithFetchStatus(stamp(7), "epoch-a", {{"Claude", "auth"}}, {{"Claude", "expired"}}));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(8), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(9), "epoch-a"));
        QVERIFY(shown("Claude")["is_success"].toBool()); QVERIFY(!shown("Claude").contains("stale"));
        auto providers = QJsonDocument::fromJson(TestUsage::snapshotWithFetchStatus(stamp(10), "epoch-a", {{"Claude", "transient"}})).array();
        auto claude = providers[0].toObject(); auto auth = claude["auth"].toObject();
        auth["source"] = QJsonObject{{"kind", "cli"}, {"name", "Another CLI"}}; claude["auth"] = auth; providers[0] = claude;
        read(QJsonDocument(providers).toJson(QJsonDocument::Compact));
        QVERIFY(!shown("Claude").contains("stale"));
        read(TestUsage::snapshotWithFetchStatus(stamp(11), "epoch-a", {{"Claude", "transient"}}));
        QVERIFY(!shown("Claude").contains("stale"));
        // A provider that disappears loses its reading; a reappearing failure starts empty.
        read(TestUsage::snapshotWithFetchStatus(stamp(12), "epoch-a"));
        read(TestUsage::snapshotWithFetchStatus(stamp(13), "epoch-a", {{"Grok", "transient"}}));
        QVERIFY(shown("Grok")["stale"].toBool());
        providers = QJsonDocument::fromJson(TestUsage::snapshotWithFetchStatus(stamp(14), "epoch-a")).array();
        providers.removeLast();
        read(QJsonDocument(providers).toJson(QJsonDocument::Compact));
        QCOMPARE(controller.displayProviders().size(), 3);
        read(TestUsage::snapshotWithFetchStatus(stamp(15), "epoch-a", {{"Grok", "transient"}}));
        QVERIFY(!shown("Grok").contains("stale"));
        // Servers without fetch metadata never produce provider-error stale readings.
        read(TestUsage::snapshot());
        QVERIFY(!shown("Claude").contains("stale"));
        auto legacy = QJsonDocument::fromJson(TestUsage::snapshot()).array();
        auto failed = legacy[0].toObject(); failed["error"] = "Unavailable"; failed["is_success"] = false; failed["buckets"] = QJsonArray{};
        legacy[0] = failed;
        read(QJsonDocument(legacy).toJson(QJsonDocument::Compact));
        QVERIFY(!shown("Claude").contains("stale")); QVERIFY(!shown("Claude")["is_success"].toBool());
    }
    void credentialRecoveryUpdatesRetainedReading() {
        QTemporaryDir dir;
        const auto stamp = [](int offset) { return QDateTime::currentDateTimeUtc().addSecs(offset).toString(Qt::ISODateWithMs); };
        auto cursor = [&](bool failed, double used, const QString &at) {
            QJsonObject provider{{"provider_name", "Cursor"}, {"subtitle", "Sample account"},
                {"is_success", !failed}, {"needs_reauth", false},
                {"error", failed ? QJsonValue("Request timed out") : QJsonValue(QJsonValue::Null)},
                {"auth", QJsonObject{{"state", "signed_in"}, {"source", QJsonObject{{"kind", "browser"}, {"name", "Firefox"}}},
                    {"sign_in_command", QJsonValue::Null}, {"sign_in_url", QJsonValue::Null}, {"accepts_browser_credentials", true}, {"checked", QJsonArray{}}}},
                {"fetch_status", QJsonObject{{"fetched_at", at}, {"failure_kind", failed ? QJsonValue("transient") : QJsonValue(QJsonValue::Null)}, {"credential_epoch", "epoch-cursor"}}},
                {"buckets", failed ? QJsonArray{} : QJsonArray{QJsonObject{{"id", "weekly"}, {"label", "Weekly"}, {"utilization", used},
                    {"resets_at", QDateTime::currentDateTimeUtc().addDays(2).toString(Qt::ISODate)}}}}};
            return provider;
        };
        ControllerFixture controller(dir.filePath("settings.json"), QJsonDocument(QJsonArray{cursor(true, 0, stamp(0))}).toJson());
        QTRY_COMPARE(controller.providers().size(), 1);
        QVERIFY(!controller.displayProviders().first().toMap().contains("stale")); // Nothing was ever read successfully.
        auto credentials = controller.findChild<CredentialService *>(); QVERIFY(credentials);
        QVariantList replacement;
        QVERIFY(Usage::parse(QJsonDocument(QJsonArray{cursor(false, 42, stamp(1))}).toJson(), replacement));
        credentials->providerRecovered(replacement.first().toMap());
        QVERIFY(controller.providers().first().toMap()["is_success"].toBool());
        // The recovered reading is the retained one when the provider fails again.
        controller.replaceSnapshot(QJsonDocument(QJsonArray{cursor(true, 0, stamp(2))}).toJson());
        const auto shown = controller.displayProviders().first().toMap();
        QVERIFY(shown["stale"].toBool());
        QCOMPARE(shown["buckets"].toList().first().toMap()["utilization"].toDouble(), 42.0);
        QVERIFY(controller.providers().first().toMap()["buckets"].toList().isEmpty());
    }
};
QTEST_GUILESS_MAIN(ProviderReadingsTest)
#include "test_providerreadings.moc"
