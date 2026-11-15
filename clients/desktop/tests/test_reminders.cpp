#include "reminders.h"
#include <QtTest>
#include <QTemporaryDir>
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <algorithm>
#include "statemachine_assertions.h"

class RemindersTest : public QObject {
    Q_OBJECT
    inline static const QDateTime now = QDateTime::fromString("2026-10-02T12:00:00Z", Qt::ISODate);
    inline static const QDateTime expiry = now.addSecs(12 * 3600);
    static QVariantMap bucket(const QString &id, double used, const QDateTime &reset,
                              const QString &label = {}) {
        return {{"id", id}, {"label", label.isEmpty() ? (id == "weekly" ? "Weekly" : "Session") : label},
            {"utilization", used},
            {"resets_at", reset.isValid() ? QVariant(reset.toString(Qt::ISODateWithMs)) : QVariant()}};
    }
    static QVariantMap kind(const QString &label, int count, const QDateTime &expires) {
        return {{"id", "weekly"}, {"label", label}, {"available_count", count}, {"usable_now", true},
            {"expires_at", expires.isValid() ? QVariant(expires.toString(Qt::ISODateWithMs)) : QVariant()}};
    }
    static QVariantMap credits(const QVariantList &kinds, const QString &fingerprint = {}) {
        return {{"available_count", kinds.size()}, {"account_fingerprint", fingerprint},
            {"usable_now", true}, {"kinds", kinds}};
    }
    static QVariantMap window(int count, const QDateTime &from, const QDateTime &expires) {
        return {{"count", count},
            {"valid_from", from.isValid() ? QVariant(from.toString(Qt::ISODateWithMs)) : QVariant()},
            {"expires_at", expires.isValid() ? QVariant(expires.toString(Qt::ISODateWithMs)) : QVariant()}};
    }
    static QVariantMap inventory(const QString &state, const QVariantList &windows) {
        return {{"reset_inventory", QVariantMap{{"state", state}, {"reported_count", windows.size()},
            {"windows", windows}}}};
    }
    static QVariantMap provider(const QVariantList &buckets = {}, const QString &name = "Claude",
                                const QVariantMap &creditMap = {}, const QVariantMap &details = {}) {
        QVariantMap result{{"provider_name", name}, {"is_success", true}, {"needs_reauth", false},
            {"buckets", buckets}};
        if (!creditMap.isEmpty()) result["rate_limit_reset_credits"] = creditMap;
        if (!details.isEmpty()) result["usage_details"] = details;
        return result;
    }
    static QVariantMap sessionProvider(double used, const QDateTime &reset, const QString &fingerprint = {},
                                       const QString &name = "Claude") {
        return provider({bucket("session", used, reset)}, name, credits({}, fingerprint));
    }
    static QByteArray readAll(const QString &path) {
        QFile file(path);
        if (!file.open(QIODevice::ReadOnly)) return {};
        return file.readAll();
    }
    static bool writeAll(const QString &path, const QByteArray &bytes) {
        QFile file(path);
        return file.open(QIODevice::WriteOnly) && file.write(bytes) == bytes.size();
    }
private slots:
    void init() {
        failOnStateMachineWarning();
    }
    void expiryLeadsFireOnce() {
        const auto reset = now.addSecs(24 * 3600);
        const QVariantList rows{provider({}, "Claude", credits({kind("weekly reset", 1, reset)}))};

        Reminders reminders;
        const auto day = reminders.evaluate(rows, now, true, true);
        QCOMPARE(day.size(), 1);
        QVERIFY(day.first().kind == Reminder::Kind::BankedExpiry);
        QCOMPARE(day.first().severity, 0);
        QCOMPARE(day.first().target, QString("bankedResets_Claude"));
        QVERIFY(reminders.evaluate(rows, now, true, true).isEmpty());

        const auto hour = reminders.evaluate(rows, now.addSecs(23 * 3600), true, true);
        QCOMPARE(hour.size(), 1);
        QCOMPARE(hour.first().severity, 2);
        QVERIFY(reminders.evaluate(rows, now.addSecs(23 * 3600), true, true).isEmpty());

        Reminders late;
        const QVariantList insideHour{provider({}, "Claude", credits({kind("5-hour reset", 2, now.addSecs(1800))}))};
        const auto once = late.evaluate(insideHour, now, true, true);
        QCOMPARE(once.size(), 1);
        QCOMPARE(once.first().severity, 2);
        QVERIFY(late.evaluate(insideHour, now, true, true).isEmpty());

        Reminders past;
        const QVariantList expired{provider({}, "Claude", credits({kind("weekly reset", 1, now.addSecs(-60))}))};
        QVERIFY(past.evaluate(expired, now, true, true).isEmpty());
    }
    void expiryGroupsKindsWithinMinute() {
        const auto first = now.addSecs(12 * 3600), second = first.addSecs(30);
        const QVariantList both{provider({}, "Claude", credits({kind("weekly reset", 1, first),
            kind("5-hour reset", 1, second)}))};
        Reminders reminders;
        const auto seen = reminders.evaluate(both, now, true, true);
        QCOMPARE(seen.size(), 1);
        QCOMPARE(seen.first().kind, Reminder::Kind::BankedExpiry);
        QCOMPARE(seen.first().target, QString("bankedResets_Claude"));

        const QVariantList onlyLater{provider({}, "Claude", credits({kind("5-hour reset", 1, second)}))};
        QVERIFY(reminders.evaluate(onlyLater, now, true, true).isEmpty());

        Reminders ignored;
        const QVariantList unusable{provider({}, "Claude", credits({kind("weekly reset", 0, first),
            kind("5-hour reset", 1, QDateTime())}))};
        QVERIFY(ignored.evaluate(unusable, now, true, true).isEmpty());
        auto failed = both.first().toMap();
        failed["is_success"] = false;
        QVERIFY(ignored.evaluate({failed}, now, true, true).isEmpty());
    }
    void inventoryExpiry() {
        Reminders reminders;
        const auto seen = reminders.evaluate({provider({}, "Grok", {},
            inventory("ok", {window(2, now.addSecs(-3600), expiry)}))}, now, true, true);
        QCOMPARE(seen.size(), 1);
        QVERIFY(seen.first().kind == Reminder::Kind::BankedExpiry);
        QCOMPARE(seen.first().target, QString("providerCard_Grok"));
        QCOMPARE(seen.first().severity, 0);

        Reminders ignored;
        QVERIFY(ignored.evaluate({provider({}, "Grok", {},
            inventory("stale", {window(2, now.addSecs(-60), expiry)}))}, now, true, true).isEmpty());
        QVERIFY(ignored.evaluate({provider({}, "Grok", {},
            inventory("ok", {window(2, now.addSecs(3600), now.addSecs(7200))}))}, now, true, true).isEmpty());
        QVERIFY(ignored.evaluate({provider({}, "Grok", {},
            inventory("ok", {window(2, QDateTime(), expiry)}))}, now, true, true).isEmpty());
    }
    void unusedAllowance() {
        for (const double used : {0.0, 75.0}) {
            Reminders reminders;
            const auto seen = reminders.evaluate({provider({bucket("weekly", used, expiry)})}, now, true, true);
            QCOMPARE(seen.size(), 1);
            QVERIFY(seen.first().kind == Reminder::Kind::UnusedAllowance);
            QCOMPARE(seen.first().severity, 0);
            QCOMPARE(seen.first().target, QString("meter_Claude_weekly"));
        }
        Reminders edge;
        QVERIFY(edge.evaluate({provider({bucket("weekly", 75.1, expiry)})}, now, true, true).isEmpty());

        for (const bool notify : {false, true}) {
            Reminders silent;
            const QVariantList rows{provider({bucket("weekly", 40, expiry)})};
            QVERIFY(silent.evaluate(rows, now, notify, !notify).isEmpty());
            QVERIFY(silent.evaluate(rows, now, true, true).isEmpty());
        }
    }
    void sessionEpisodes() {
        const auto reset = now.addSecs(2 * 3600);
        Reminders reminders;
        const auto depleted = reminders.evaluate({sessionProvider(100, reset)}, now, true, true);
        QCOMPARE(depleted.size(), 1);
        QVERIFY(depleted.first().kind == Reminder::Kind::SessionDepleted);
        QCOMPARE(depleted.first().severity, 3);
        QCOMPARE(depleted.first().target, QString("meter_Claude_session"));
        QVERIFY(reminders.evaluate({sessionProvider(100, reset)}, now.addSecs(60), true, true).isEmpty());
        const auto restored = reminders.evaluate({sessionProvider(99.4, reset)}, now.addSecs(120), true, true);
        QCOMPARE(restored.size(), 1);
        QVERIFY(restored.first().kind == Reminder::Kind::SessionRestored);
        QCOMPARE(restored.first().severity, 0);

        struct SuppressedRecovery { double used; QDateTime observed; QString fingerprint; };
        for (const auto &row : {SuppressedRecovery{99.6, now.addSecs(120), QString(64, 'a')},
                 SuppressedRecovery{50, reset.addSecs(3601), QString(64, 'a')},
                 SuppressedRecovery{50, now.addSecs(120), QString(64, 'b')}}) {
            Reminders suppressed;
            QCOMPARE(suppressed.evaluate({sessionProvider(100, reset, QString(64, 'a'))}, now, true, true).size(), 1);
            QVERIFY(suppressed.evaluate({sessionProvider(row.used, reset, row.fingerprint)},
                row.observed, true, true).isEmpty());
        }

        Reminders secondEpisode;
        secondEpisode.evaluate({sessionProvider(100, reset)}, now, true, true);
        const auto again = secondEpisode.evaluate({sessionProvider(100, now.addSecs(5 * 3600))},
            now.addSecs(60), true, true);
        QCOMPARE(again.size(), 1);
        QVERIFY(again.first().kind == Reminder::Kind::SessionDepleted);
        const auto onlyNew = secondEpisode.evaluate({sessionProvider(50, now.addSecs(5 * 3600))},
            now.addSecs(120), true, true);
        QCOMPARE(onlyNew.size(), 1);
        QVERIFY(onlyNew.first().kind == Reminder::Kind::SessionRestored);

        Reminders longWindow;
        for (const QString name : {QString("Cursor"), QString("Grok")})
            QVERIFY(longWindow.evaluate({provider({bucket("session", 100, reset, "Monthly")}, name)},
                now, true, true).isEmpty());
    }
    void persistence() {
        QTemporaryDir directory;
        QVERIFY(directory.isValid());
        const QString path = directory.filePath("reminders.json");
        const QVariantList rows{provider({}, "Claude", credits({kind("weekly reset", 1, expiry)}))};
        QCOMPARE(Reminders(path).evaluate(rows, now, true, true).size(), 1);
        Reminders second(path);
        QVERIFY(second.evaluate(rows, now, true, true).isEmpty());
        for (const int offset : {-1, 1}) {
            const QVariantList corrected{provider({}, "Claude", credits({kind("weekly reset", 1, expiry.addSecs(offset))}))};
            QVERIFY(second.evaluate(corrected, now, true, true).isEmpty());
        }
        const QVariantList moved{provider({}, "Claude", credits({kind("weekly reset", 1, expiry.addSecs(300))}))};
        QCOMPARE(second.evaluate(moved, now, true, true).size(), 1);

        const QString sessionPath = directory.filePath("session.json");
        const auto sessionReset = now.addSecs(2 * 3600);
        QCOMPARE(Reminders(sessionPath).evaluate({sessionProvider(100, sessionReset)}, now, true, true).size(), 1);
        Reminders session(sessionPath);
        const auto seen = session.evaluate({sessionProvider(50, sessionReset)}, now.addSecs(60), true, true);
        QCOMPARE(seen.size(), 1);
        QVERIFY(seen.first().kind == Reminder::Kind::SessionRestored);
        QVERIFY(session.evaluate({sessionProvider(50, sessionReset)}, now.addSecs(60), true, true).isEmpty());
    }
    void ledgerRecoveryRetentionAndRedaction() {
        QTemporaryDir directory;
        QVERIFY(directory.isValid());

        const QString corrupt = directory.filePath("corrupt.json");
        QVERIFY(writeAll(corrupt, "not json"));
        Reminders recovered(corrupt);
        QVERIFY(!recovered.error().isEmpty());
        const QVariantList rows{provider({}, "Claude", credits({kind("weekly reset", 1, expiry)}))};
        QCOMPARE(recovered.evaluate(rows, now, true, true).size(), 1);
        QVERIFY(recovered.error().isEmpty());
        const auto document = QJsonDocument::fromJson(readAll(corrupt));
        QVERIFY(document.isObject());
        QCOMPARE(document.object().value("schemaVersion").toInt(), 1);
        QCOMPARE(document.object().value("records").toArray().size(), 1);

        const QString newer = directory.filePath("newer.json");
        const QByteArray newerBytes =
            R"({"schemaVersion":7,"records":[{"scope":"0123456789abcdef0123456789abcdef","type":"expiry","meter":"","boundary":1,"phases":3}]})";
        QVERIFY(writeAll(newer, newerBytes));
        Reminders readOnly(newer);
        QVERIFY(!readOnly.error().isEmpty());
        QCOMPARE(readOnly.evaluate(rows, now, true, true).size(), 1);
        QCOMPARE(readAll(newer), newerBytes);

        const QString capped = directory.filePath("capped.json");
        QJsonArray ledger;
        const auto base = now.addSecs(-3600);
        for (int i = 0; i < 600; ++i) {
            ledger.append(QJsonObject{{"scope", QString("%1").arg(i + 1, 32, 16, QChar('0'))},
                {"type", "expiry"}, {"meter", ""}, {"boundary", qint64(base.addSecs(-i).toMSecsSinceEpoch())},
                {"phases", 3}});
        }
        const qint64 future = now.addSecs(10 * 86400).toMSecsSinceEpoch();
        ledger.append(QJsonObject{{"scope", QString("ffffffffffffffffffffffffffffffff")},
            {"type", "expiry"}, {"meter", ""}, {"boundary", future}, {"phases", 0}});
        QVERIFY(writeAll(capped, QJsonDocument(QJsonObject{{"schemaVersion", 1}, {"records", ledger}})
            .toJson(QJsonDocument::Compact)));
        Reminders cappedReminders(capped);
        QVERIFY(cappedReminders.error().isEmpty());
        QVERIFY(cappedReminders.evaluate({}, now, true, true).isEmpty());
        const auto kept = QJsonDocument::fromJson(readAll(capped)).object().value("records").toArray();
        QCOMPARE(kept.size(), 512);
        QVERIFY(std::any_of(kept.begin(), kept.end(), [future](const QJsonValue &entry) {
            return entry.toObject().value("boundary").toInteger() == future;
        }));

        const QString redacted = directory.filePath("redacted.json");
        const QString fingerprint(64, 'a');
        Reminders privateLedger(redacted);
        QCOMPARE(privateLedger.evaluate({provider({bucket("weekly", 10, expiry)}, "Claude",
            credits({kind("weekly reset", 1, expiry)}, fingerprint))}, now, true, true).size(), 2);
        QVERIFY(privateLedger.error().isEmpty());
        const QByteArray stored = readAll(redacted);
        QVERIFY(!stored.contains("Claude"));
        QVERIFY(!stored.contains("weekly reset"));
        QVERIFY(!stored.contains("Weekly"));
        QVERIFY(!stored.contains(fingerprint.toLatin1()));
#ifndef Q_OS_WIN
        const auto mode = QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner
            | QFileDevice::ReadGroup | QFileDevice::WriteGroup | QFileDevice::ExeGroup
            | QFileDevice::ReadOther | QFileDevice::WriteOther | QFileDevice::ExeOther;
        QCOMPARE(QFile::permissions(redacted) & mode, QFileDevice::ReadOwner | QFileDevice::WriteOwner);
#endif
    }
};

QTEST_MAIN(RemindersTest)
#include "test_reminders.moc"
