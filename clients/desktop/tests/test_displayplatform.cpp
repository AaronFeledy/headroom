#include "displayplatform.h"
#include <QtTest>

class DisplayPlatformTest : public QObject {
    Q_OBJECT
private slots:
    void selection_data() {
        QTest::addColumn<QString>("session");
        QTest::addColumn<QString>("waylandDisplay");
        QTest::addColumn<QString>("xDisplay");
        QTest::addColumn<QString>("overridePlatform");
        QTest::addColumn<bool>("hasLayerShell");
        QTest::addColumn<QByteArray>("expected");
        QTest::newRow("release-on-wayland") << "wayland" << "wayland-0" << ":1" << "" << false << QByteArray("xcb;wayland");
        QTest::newRow("wayland-without-session-variable") << "" << "wayland-1" << ":2" << "" << false << QByteArray("xcb;wayland");
        QTest::newRow("wayland-without-display-variable") << "wayland" << "" << ":1" << "" << false << QByteArray("xcb;wayland");
        QTest::newRow("native-layer-shell") << "wayland" << "wayland-0" << ":1" << "" << true << QByteArray();
        QTest::newRow("wayland-without-xwayland") << "wayland" << "wayland-0" << "" << "" << false << QByteArray();
        QTest::newRow("native-x11") << "x11" << "" << ":0" << "" << false << QByteArray();
        QTest::newRow("headless") << "" << "" << "" << "" << false << QByteArray();
        QTest::newRow("explicit-wayland") << "wayland" << "wayland-0" << ":1" << "wayland" << false << QByteArray();
        QTest::newRow("explicit-offscreen") << "wayland" << "wayland-0" << ":1" << "offscreen" << false << QByteArray();
        QTest::newRow("explicit-platform-list") << "wayland" << "wayland-0" << ":1" << "wayland;xcb" << false << QByteArray();
    }
    void selection() {
        QFETCH(QString, session); QFETCH(QString, waylandDisplay); QFETCH(QString, xDisplay);
        QFETCH(QString, overridePlatform); QFETCH(bool, hasLayerShell); QFETCH(QByteArray, expected);
        QProcessEnvironment environment;
        environment.insert("XDG_SESSION_TYPE", session);
        environment.insert("WAYLAND_DISPLAY", waylandDisplay);
        environment.insert("DISPLAY", xDisplay);
        environment.insert("QT_QPA_PLATFORM", overridePlatform);
        QCOMPARE(DesktopPlatform::preferredPlatform(environment, hasLayerShell), expected);
    }
};

QTEST_APPLESS_MAIN(DisplayPlatformTest)
#include "test_displayplatform.moc"
