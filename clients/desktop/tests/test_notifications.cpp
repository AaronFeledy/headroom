#include "notifications.h"
#include <QtTest>

class NotificationsTest : public QObject {
    Q_OBJECT
private slots:
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
