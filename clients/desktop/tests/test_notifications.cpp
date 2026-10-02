#include "notifications.h"
#include <QtTest>
#include <limits>

class NotificationsTest : public QObject {
    Q_OBJECT
    static QDateTime time() {
        return QDateTime::fromString("2026-10-02T12:00:00Z", Qt::ISODate);
    }
    static QVariantMap bucket(double used, QDateTime reset, const QString &id = "weekly") {
        return {{"id", id}, {"label", id == "weekly" ? "Weekly" : "Session"}, {"utilization", used},
            {"resets_at", reset.isValid() ? QVariant(reset.toString(Qt::ISODateWithMs)) : QVariant()}};
    }
    static QVariantMap provider(const QVariantList &buckets, const QString &name = "Claude",
                                const QString &fingerprint = {}) {
        return {{"provider_name", name}, {"is_success", true}, {"needs_reauth", false}, {"buckets", buckets},
            {"rate_limit_reset_credits", QVariantMap{{"account_fingerprint", fingerprint}}}};
    }
private slots:
    void earlyUsageResetAcrossProvidersAndWindowChanges_data() {
        QTest::addColumn<QString>("name");
        QTest::addColumn<int>("newDeadline");
        for (const auto &name : {"Claude", "Codex", "Cursor", "Grok"}) {
            QTest::newRow(qPrintable(QString(name) + "-unchanged")) << QString(name) << 86400;
            QTest::newRow(qPrintable(QString(name) + "-restarted")) << QString(name) << 7 * 86400;
            QTest::newRow(qPrintable(QString(name) + "-idle")) << QString(name) << 0;
        }
    }
    void earlyUsageResetAcrossProvidersAndWindowChanges() {
        QFETCH(QString, name); QFETCH(int, newDeadline);
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        const auto now = time();
        notifications.observeUsage({provider({bucket(42, now.addDays(1))}, name)}, true, now);
        auto cleared = bucket(0, newDeadline ? now.addSecs(newDeadline) : QDateTime());
        if (newDeadline == 7 * 86400) cleared["starts_at"] = now.toString(Qt::ISODate);
        const QVariantList snapshot {provider({cleared}, name)};
        notifications.observeUsage(snapshot, true, now.addSecs(60));
        QCOMPARE(native.size(), 1);
        QCOMPARE(notifications.unreadCount(), 1);
        QCOMPARE(native.first()[2].toInt(), 0); // Informational, not a warning.
        if (name == "Codex") QVERIFY(native.first()[0].toString().startsWith("ChatGPT"));
        notifications.present();
        const auto event = notifications.presented().first().toMap();
        QCOMPARE(event["target"].toString(), "meter_" + name + "_weekly");
        QVERIFY(event["message"].toString().contains("42.0% to 0%"));
        QVERIFY(notifications.claimHighlight(event["target"].toString()));
        QVERIFY(!notifications.claimHighlight(event["target"].toString()));
        notifications.endPresentation();
        notifications.observeUsage(snapshot, true, now.addSecs(120));
        QCOMPARE(native.size(), 1);
        QCOMPARE(notifications.unreadCount(), 0);
        notifications.present();
        QVERIFY(notifications.presented().isEmpty());
    }
    void scheduledUsageResetsStayQuiet_data() {
        QTest::addColumn<int>("remaining");
        QTest::addColumn<bool>("restarted");
        for (const int seconds : {-3600, -60, 0, 60, 899, 900, 901}) {
            QTest::newRow(qPrintable(QString::number(seconds) + "-same")) << seconds << false;
            QTest::newRow(qPrintable(QString::number(seconds) + "-new")) << seconds << true;
        }
    }
    void scheduledUsageResetsStayQuiet() {
        QFETCH(int, remaining); QFETCH(bool, restarted);
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        const auto now = time(), oldDeadline = now.addSecs(remaining);
        notifications.observeUsage({provider({bucket(75, oldDeadline)})}, true, now.addSecs(-7200));
        notifications.observeUsage({provider({bucket(0, restarted ? now.addDays(7) : oldDeadline)})}, true, now);
        QCOMPARE(native.size(), remaining > 900 ? 1 : 0);
    }
    void onlyObservedZeroWithKnownOldDeadlineNotifies() {
        const auto now = time();
        for (const double used : {0.0, 0.01, 20.0, 41.9, 42.0, 43.0}) {
            Notifications notifications;
            QSignalSpy native(&notifications, &Notifications::desktopNotification);
            notifications.observeUsage({provider({bucket(42, now.addDays(1))})}, true, now);
            notifications.observeUsage({provider({bucket(used, now.addDays(7))})}, true, now.addSecs(60));
            QCOMPARE(native.size(), used == 0 ? 1 : 0);
        }
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.observeUsage({provider({bucket(42, {})})}, true, now);
        notifications.observeUsage({provider({bucket(0, now.addDays(7))})}, true, now.addSecs(60));
        notifications.observeUsage({provider({bucket(0, now.addDays(8))})}, true, now.addSecs(120));
        QCOMPARE(native.size(), 0); // Unknown prior schedule and zero-to-zero are not evidence.
    }
    void missingAndFailedReadingsNeverBecomeZero() {
        const auto now = time(), reset = now.addDays(1);
        const QVariantList high {provider({bucket(42, reset)})}, zero {provider({bucket(0, reset)})};
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.observeUsage(high, true, now);
        auto failed = provider({bucket(0, reset)}); failed["is_success"] = false;
        notifications.observeUsage({failed}, true, now.addSecs(60));
        QCOMPARE(native.size(), 0);
        notifications.observeUsage(zero, true, now.addSecs(120));
        QCOMPARE(native.size(), 1); // A transient error preserved the good baseline.
        notifications.resetUsageBaseline();
        for (const QVariantList missing : {QVariantList{}, QVariantList{provider({})}}) {
            notifications.observeUsage(high, true, now);
            notifications.observeUsage(missing, true, now.addSecs(60));
            notifications.observeUsage(zero, true, now.addSecs(120));
            QCOMPARE(native.size(), 1); // Removed provider/meter establishes a new baseline.
        }
        for (const QVariant invalid : {QVariant(), QVariant("bad"), QVariant(-1),
                                      QVariant(std::numeric_limits<double>::quiet_NaN())}) {
            notifications.observeUsage(high, true, now);
            auto unavailable = bucket(0, reset); unavailable["utilization"] = invalid;
            notifications.observeUsage({provider({unavailable})}, true, now.addSecs(60));
            QCOMPARE(native.size(), 1);
        }
    }
    void baselinesFollowAccountsSettingsAndNotificationPreference() {
        const auto now = time(), reset = now.addDays(1);
        auto snapshot = [&](double used, QString account = "a") {
            return QVariantList{provider({bucket(used, reset)}, "Codex", account)};
        };
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.observeUsage(snapshot(42), true, now);
        notifications.observeUsage(snapshot(0), false, now.addSecs(60));
        notifications.observeUsage(snapshot(0), true, now.addSecs(120));
        QCOMPARE(native.size(), 0);
        notifications.observeUsage(snapshot(42), true, now.addSecs(180));
        notifications.observeUsage(snapshot(0, "b"), true, now.addSecs(240));
        QCOMPARE(native.size(), 0);
        notifications.observeUsage(snapshot(42, "b"), true, now.addSecs(300));
        notifications.observeUsage(snapshot(0, "b"), true, now.addSecs(360));
        QCOMPARE(native.size(), 1);
        notifications.present();
        QVERIFY(!notifications.presented().first().toMap().contains("account_fingerprint"));
        notifications.observeUsage(snapshot(42, "c"), true, now.addSecs(420));
        QVERIFY(notifications.presented().isEmpty());
        QVERIFY(!notifications.claimHighlight("meter_Codex_weekly"));
        notifications.resetUsageBaseline();
        notifications.observeUsage(snapshot(0, "c"), true, now.addSecs(480));
        QCOMPARE(native.size(), 1);
        notifications.observeUsage(snapshot(42, "c"), true, now.addSecs(540));
        auto reauth = snapshot(0, "c").first().toMap();
        reauth["is_success"] = false; reauth["needs_reauth"] = true;
        notifications.observeUsage({reauth}, true, now.addSecs(600));
        notifications.observeUsage(snapshot(0, "c"), true, now.addSecs(660));
        QCOMPARE(native.size(), 1);
        notifications.observeUsage(snapshot(42, "c"), true, now.addSecs(720));
        auto planChange = snapshot(0, "c").first().toMap(); planChange["subtitle"] = "New plan";
        notifications.observeUsage({planChange}, true, now.addSecs(780));
        QCOMPARE(native.size(), 1);
    }
    void usageMetersAreIndependentAndConnectionCleanupPreservesOtherEvents() {
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        const auto now = time(), reset = now.addDays(1);
        notifications.observeUsage({provider({bucket(42, reset), bucket(24, reset, "session")}),
            provider({bucket(64, reset)}, "Codex")}, true, now);
        notifications.observeUsage({provider({bucket(64, reset)}, "Codex"),
            provider({bucket(0, reset, "session"), bucket(0, reset)})}, true, now.addSecs(60));
        QCOMPARE(native.size(), 2);
        QCOMPARE(notifications.unreadCount(), 2);
        notifications.present();
        QVERIFY(notifications.claimHighlight("meter_Claude_session"));
        QVERIFY(notifications.claimHighlight("meter_Claude_weekly"));
        notifications.post("desktopUpdate", "Update", "Available");
        notifications.resetUsageBaseline();
        QCOMPARE(notifications.unreadCount(), 1);
        QVERIFY(notifications.presented().isEmpty());
        notifications.observeUsage({provider({bucket(0, reset)}, "Codex")}, true, now.addSecs(120));
        QCOMPARE(native.size(), 3); // Only the unrelated update added an alert.
    }
    void bankedResetChangesUseObservedCountsAndAccountBaselines() {
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.observeBankedResetCount(3, "account-a", true);
        notifications.observeBankedResetCount(3, "account-a", true);
        notifications.observeBankedResetCount(-1, "account-a", true);
        QCOMPARE(native.size(), 0);
        notifications.observeBankedResetCount(4, "account-a", true);
        QCOMPARE(native.size(), 1);
        QCOMPARE(notifications.unreadCount(), 1);
        notifications.present();
        auto event = notifications.presented().first().toMap();
        QCOMPARE(event["target"].toString(), "bankedResets_Codex");
        QCOMPARE(event["delta"].toLongLong(), 1);
        QVERIFY(event["message"].toString().contains("3 to 4"));
        QVERIFY(!event.values().contains("account-a"));
        notifications.endPresentation();
        notifications.observeBankedResetCount(1, "account-a", true);
        notifications.observeBankedResetCount(0, "account-a", true);
        QCOMPARE(native.size(), 3);
        QCOMPARE(notifications.unreadCount(), 1); // Latest change for this counter wins.
        notifications.present();
        event = notifications.presented().first().toMap();
        QCOMPARE(event["delta"].toLongLong(), -1);
        QCOMPARE(event["count"].toLongLong(), 0);
        notifications.observeBankedResetCount(8, "account-b", true);
        QCOMPARE(native.size(), 3);
        QVERIFY(notifications.presented().isEmpty()); // Old-account attention cannot leak.
        notifications.observeBankedResetCount(9, "account-b", false);
        notifications.observeBankedResetCount(9, "account-b", true);
        QCOMPARE(native.size(), 3);
        notifications.observeBankedResetCount(11, "account-b", true);
        notifications.present();
        QCOMPARE(notifications.presented().first().toMap()["delta"].toLongLong(), 2);
        notifications.resetBankedResetBaseline();
        notifications.observeBankedResetCount(1, "account-b", true);
        QCOMPARE(native.size(), 4);
        QCOMPARE(notifications.unreadCount(), 0);
        QVERIFY(notifications.presented().isEmpty());
    }
    void coalescesTargetsAndConsumesOnce() {
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.post("meter_Claude_session", "Warning", "First", 2);
        notifications.post("meter_Claude_session", "Critical", "Escalated", 3);
        notifications.post("meter_Codex_weekly", "Warning", "Different meter", 2);
        QCOMPARE(notifications.unreadCount(), 2);
        QCOMPARE(native.size(), 3);
        notifications.present();
        QCOMPARE(notifications.unreadCount(), 0);
        QCOMPARE(notifications.presented().size(), 2);
        QCOMPARE(notifications.presented().first().toMap()["message"].toString(), "Escalated");
        QVERIFY(notifications.claimHighlight("meter_Claude_session"));
        QVERIFY(!notifications.claimHighlight("meter_Claude_session"));
        notifications.present();
        QVERIFY(notifications.claimHighlight("meter_Codex_weekly"));
        notifications.endPresentation();
        notifications.present();
        QVERIFY(notifications.presented().isEmpty());
        QVERIFY(!notifications.claimHighlight("meter_Claude_session"));
        notifications.post("meter_Claude_session", "Warning", "New episode", 2);
        notifications.present();
        QVERIFY(notifications.claimHighlight("meter_Claude_session"));
    }
    void hiddenEventsSurvivePresentationEndAndStateIsBounded() {
        Notifications notifications;
        for (int i = 0; i < Notifications::MaxPending + 10; ++i)
            notifications.post(QString::number(i), "Warning", "Synthetic");
        QCOMPARE(notifications.unreadCount(), Notifications::MaxPending);
        notifications.endPresentation();
        QCOMPARE(notifications.unreadCount(), Notifications::MaxPending);
        notifications.present();
        QVERIFY(!notifications.claimHighlight("0"));
        QVERIFY(notifications.claimHighlight(QString::number(Notifications::MaxPending + 9)));
        notifications.post("new", "Warning", "Synthetic");
        notifications.present();
        QCOMPARE(notifications.presented().size(), Notifications::MaxPending);
        QVERIFY(!notifications.claimHighlight("10"));
    }
    void updateChecksDoNotRearmSeenVersions() {
        Notifications notifications;
        QSignalSpy native(&notifications, &Notifications::desktopNotification);
        notifications.observeUpdate("checking", "", "Checking", true);
        notifications.observeUpdate("available", "8.5.0", "Available", true);
        QCOMPARE(notifications.unreadCount(), 1);
        notifications.present(); notifications.endPresentation();
        notifications.observeUpdate("checking", "8.5.0", "Checking", true);
        notifications.observeUpdate("available", "8.5.0", "Available", true);
        QCOMPARE(notifications.unreadCount(), 0);
        notifications.observeUpdate("staged", "8.5.0", "Restart", true);
        QCOMPARE(notifications.unreadCount(), 1);
        notifications.observeUpdate("failed", "8.5.0", "Could not apply", true);
        QCOMPARE(notifications.unreadCount(), 1);
        QCOMPARE(native.size(), 3);
        notifications.present(); notifications.endPresentation();
        notifications.observeUpdate("available", "8.6.0", "Available", false);
        notifications.observeUpdate("available", "8.6.0", "Available", true);
        QCOMPARE(notifications.unreadCount(), 0);
        notifications.observeUpdate("available", "8.7.0", "Available", true);
        QCOMPARE(notifications.unreadCount(), 1);
    }
};
QTEST_MAIN(NotificationsTest)
#include "test_notifications.moc"
