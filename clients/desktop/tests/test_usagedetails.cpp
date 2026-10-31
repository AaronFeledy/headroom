#include "richusagefixture.h"
#include "usagefixture.h"
#include "usagedetails.h"
#include "trayvisual.h"
#include "scripted_backend.h"
#include <QtTest>

class UsageDetailsTest : public QObject {
    Q_OBJECT
private slots:
    void summariesAndMetadataWithoutMeter() {
        QVariantList ps;
        QVERIFY(Usage::parse(TestUsage::richSnapshot(), ps));
        auto cursor = ps[0].toMap();
        cursor["buckets"] = QVariantList{};
        QVERIFY(Usage::detailRows(cursor).join('\n').contains("Grok Bot:"));
        const auto summary = Usage::detailSummary(cursor).join('\n');
        QVERIFY(summary.contains("No usage available"));
        QVERIFY(summary.contains("2 restricted grants reported"));
        QVERIFY(summary.contains("$12.34 USD"));
        QVERIFY(!summary.contains("$7.50"));
        QVERIFY(Usage::parse(TestUsage::richSnapshot("stale"), ps));
        for (const auto &row : Usage::detailSummary(ps[0].toMap()))
            QVERIFY(row.startsWith("Previously reported:"));
    }
    void grokResetInventorySummaryIsReadOnly() {
        QVariantList ps; QVERIFY(Usage::parse(TestUsage::richSnapshot(), ps));
        auto grok = ps[1].toMap(); QCOMPARE(grok["provider_name"].toString(), QString("Grok"));
        auto summary = Usage::detailSummary(grok).join('\n');
        QVERIFY(summary.contains("Reset inventory (read-only): 3 reported / Next expiry"));
        QVERIFY(!summary.contains("banked"));
        auto d = grok["usage_details"].toMap(); auto inventory = d["reset_inventory"].toMap();
        inventory["reported_count"] = 1; d["reset_inventory"] = inventory; grok["usage_details"] = d;
        summary = Usage::detailSummary(grok).join('\n');
        QVERIFY(summary.contains("Reset inventory (read-only): 1 reported / Next expiry"));
        QVERIFY(!summary.contains("banked"));
        QVERIFY(Usage::parse(TestUsage::richSnapshot("stale"), ps));
        QVERIFY(Usage::detailSummary(ps[1].toMap()).join('\n').contains("Previously reported: Reset inventory (read-only): 3 reported"));
    }
    void legacyAndRich() {
        QVariantList ps;
        QVERIFY(Usage::parse(TestUsage::snapshot(), ps));
        for (const auto &p : ps) QVERIFY(p.toMap()["usage_details"].isNull());
        const auto monthly = ps[2].toMap()["buckets"].toList()[0].toMap();
        QCOMPARE(monthly["id"].toString(), QString("auto"));
        QCOMPARE(Usage::period("Cursor", monthly)["seconds"].toLongLong(), 30LL * 86400);
        QVERIFY(Usage::parse(TestUsage::richSnapshot(), ps));
        const auto cursor = ps[0].toMap();
        QCOMPARE(cursor["buckets"].toList()[0].toMap()["id"].toString(), QString("weekly"));
        QCOMPARE(Usage::period("Cursor", cursor["buckets"].toList()[0].toMap())["seconds"].toLongLong(), 7LL * 86400);
        const auto text = Usage::detailRows(cursor).join('\n');
        QVERIFY(text.contains("$0.00 USD / $20.00 USD cap"));
        QVERIFY(text.contains("$12.34 USD"));
        QVERIFY(text.contains("Auto top-up disabled"));
        QVERIFY(text.contains("Expired"));
        QVERIFY(text.contains("Sand · No usage available · Team shared pool"));
        QVERIFY(text.contains("18.5% (not a quota)"));
        QVERIFY(!text.contains("Reset inventory"));
        QVERIFY(Usage::detailRows(ps[1].toMap()).join('\n').contains("0 in valid windows at observation"));
    }
    void locallyMalformed() {
        auto details = TestUsage::richDetails();
        details["plan"] = QJsonObject{{"state", "ok"}, {"label", "<b>person@example.com</b>"}};
        auto finance = details["finances"].toObject();
        auto items = finance["items"].toArray(); auto row = items[0].toObject();
        row["included_used_minor"] = 9007199254740992.0; row["extra_cap_minor"] = 1.5;
        row["prepaid_remaining_minor"] = -1; row["resets_at"] = "tomorrow";
        row["scope"] = QJsonObject{{"kind", "unknown"}, {"shared_pool", true}};
        items[0] = row; finance["items"] = items; details["finances"] = finance;
        details["credit_grants"] = "broken";
        auto p = QJsonDocument::fromJson(TestUsage::snapshot()).array()[0].toObject(); p["usage_details"] = details;
        QVariantList ps; QVERIFY(Usage::parse(QJsonDocument(QJsonArray{p}).toJson(), ps));
        const auto normalized = ps[0].toMap()["usage_details"].toMap();
        QVERIFY(normalized["plan"].toMap()["label"].isNull());
        QVERIFY(normalized["credit_grants"].isNull());
        const auto f = normalized["finances"].toMap()["items"].toList()[0].toMap();
        for (const auto &k : {"included_used_minor", "extra_cap_minor", "prepaid_remaining_minor", "resets_at"}) QVERIFY(f[k].isNull());
        QVERIFY(!Usage::detailRows(ps[0].toMap()).join('\n').contains("shared pool"));
        QJsonArray tooMany; for (int i = 0; i < 65; ++i) tooMany.append(row);
        finance["items"] = tooMany; details["finances"] = finance;
        QVERIFY(Usage::normalizeDetails(details).toObject()["finances"].toObject()["items"].isNull());
        details["quotas"] = QJsonArray{};
        QVERIFY(Usage::normalizeDetails(details).toObject()["quotas"].isArray());
        details["quotas"] = QJsonValue::Null;
        QVERIFY(Usage::normalizeDetails(details).toObject()["quotas"].isNull());
    }
    void staleUnknownAndErrors() {
        QVariantList ps; QVERIFY(Usage::parse(TestUsage::richSnapshot("stale"), ps));
        for (const auto &p : ps) {
            const auto rows = Usage::detailRows(p.toMap()); QVERIFY(!rows.isEmpty());
            for (const auto &r : rows) QVERIFY(r.startsWith("Previously reported:"));
            QVERIFY(!rows.join('\n').contains("in valid windows"));
            QVERIFY(!rows.join('\n').contains("No usage available"));
        }
        QVERIFY(Usage::parse(TestUsage::richSnapshot("unknown"), ps));
        for (const auto &p : ps) QVERIFY(Usage::detailRows(p.toMap()).isEmpty());
        auto p = QJsonDocument::fromJson(TestUsage::richSnapshot()).array()[0].toObject();
        p["error"] = "Offline"; p["is_success"] = false;
        QVERIFY(Usage::parse(QJsonDocument(QJsonArray{p}).toJson(), ps));
        QVERIFY(ps[0].toMap()["usage_details"].isNull());
        QVERIFY(Usage::detailRows(ps[0].toMap()).isEmpty());
    }
    void financeZerosAndSand() {
        QVariantList ps; QVERIFY(Usage::parse(TestUsage::richSnapshot(), ps));
        auto p = ps[0].toMap(); auto d = p["usage_details"].toMap();
        d["plan"] = QVariantMap{}; d["credit_grants"] = QVariantMap{}; d["product_usage"] = QVariantMap{};
        d["finances"] = QVariantMap{{"state", "ok"}, {"items", QVariantList{QVariantMap{{"extra_enabled", false}, {"extra_used_minor", 0}, {"prepaid_remaining_minor", 0}}}}};
        auto quotas = d["quotas"].toList(); auto q = quotas[0].toMap(); q["has_available_usage"] = true;
        quotas[0] = q; d["quotas"] = quotas; p["usage_details"] = d;
        auto text = Usage::detailRows(p).join('\n');
        QVERIFY(!text.contains("USD")); QVERIFY(text.contains("not proof of paid access"));
        q["bucket_id"] = "weekly"; quotas[0] = q; d["quotas"] = quotas; p["usage_details"] = d;
        QVERIFY(!Usage::detailRows(p).join('\n').contains("Usage available"));
        p["buckets"] = QVariantList{};
        d["finances"] = QVariantMap{}; p["usage_details"] = d;
        QVERIFY(Usage::detailRows(p).isEmpty());
        d["plan"] = QVariantMap{{"state", "ok"}, {"label", "Pro"}};
        p["usage_details"] = d;
        QCOMPARE(Usage::detailRows(p), QStringList{"Pro"});
    }
    void backendAndConnectionMasks() {
        QTemporaryDir dir; ScriptedBackend backend; QVERIFY(backend.listen());
        bool reachable = true;
        backend.respond = [&](const QByteArray &) { return reachable ? httpResponse(200, TestUsage::richSnapshot()) : QByteArray{}; };
        Controller controller(dir.filePath("settings.json"), nullptr, false, {}, TestUsage::disabledCredentials());
        QVERIFY(controller.saveSettings("remote", backend.url(), "", 60, false, "Cursor", false).isEmpty());
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        QVERIFY(!controller.providers()[0].toMap()["usage_details"].isNull());
        reachable = false; controller.refresh();
        QTRY_COMPARE(controller.state()["status"].toString(), QString("offline"));
        for (const auto &p : controller.providers()) {
            QVERIFY(p.toMap()["usage_details"].isNull());
            QVERIFY(p.toMap()["buckets"].toList().isEmpty());
        }
        for (const auto &p : controller.displayProviders()) QVERIFY(p.toMap()["usage_details"].isNull());
        reachable = true; controller.refresh();
        QTRY_COMPARE(controller.state()["status"].toString(), QString("ready"));
        ScriptedBackend other; QVERIFY(other.listen());
        other.respond = [](const QByteArray &) { return QByteArray{}; };
        QVERIFY(controller.saveSettings("remote", other.url(), "", 60, false, "Cursor", false).isEmpty());
        for (const auto &p : controller.providers()) QVERIFY(p.toMap()["usage_details"].isNull());
        for (const auto &p : controller.displayProviders()) QVERIFY(p.toMap()["usage_details"].isNull());
    }
    void measurement_data() {
        QTest::addColumn<QVariant>("measurement"); QTest::addColumn<bool>("percent");
        QTest::newRow("legacy") << QVariant{} << false;
        QTest::newRow("null") << QVariant::fromValue(nullptr) << false;
        QTest::newRow("percent-zero") << QVariant("percent") << true;
        QTest::newRow("status") << QVariant("status") << false;
        QTest::newRow("future") << QVariant("tokens") << false;
        QTest::newRow("malformed") << QVariant(true) << false;
    }
    void measurement() {
        QFETCH(QVariant, measurement); QFETCH(bool, percent);
        QVariantMap b{{"id", "on_demand"}, {"label", "Status"}, {"status_text", "Enabled"},
            {"utilization", 0}, {"measurement", measurement}, {"resets_at", QDateTime::currentDateTimeUtc().addDays(3).toString(Qt::ISODate)}};
        QCOMPARE(Usage::isPercent(b), percent);
        QCOMPARE(Usage::pacing("Cursor", b)["available"].toBool(), percent);
        if (!percent) {
            b["utilization"] = 100;
            if (measurement.isValid() && !measurement.isNull()) {
                QCOMPARE(Usage::concern("Cursor", b)["severity"].toInt(), 0);
                QVERIFY(Usage::notches("Cursor", b).isEmpty());
                const auto tray = TrayVisual::build({{"status", "ready"}}, {QVariantMap{{"provider_name", "Cursor"}, {"is_success", true}, {"buckets", QVariantList{b}}}}, "Cursor",
                    [](const QString &name, const QVariantMap &bucket) { return Usage::concern(name, bucket); });
                QCOMPARE(tray.kind, TrayVisual::Kind::Idle);
            }
        }
    }
};
QTEST_GUILESS_MAIN(UsageDetailsTest)
#include "test_usagedetails.moc"
