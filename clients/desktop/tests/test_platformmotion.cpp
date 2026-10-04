#include "usagefixture.h"
#include "startup.h"
#include "appinfo.h"
#include "updateservice.h"
#include "remoteupdate.h"
#include "trayattention.h"
#include "palette.h"
#include <QApplication>
#include <QAccessibilityHints>
#include <QStyleHints>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQmlExpression>
#include <QQuickWindow>
#include <QQuickItem>
#include <QQuickStyle>
#include <QTemporaryDir>
#include <QtTest>

class PlatformMotionTest : public QObject {
    Q_OBJECT
private slots:
    void platformReducedMotion() {
        // Given: the native OS/portal preference, never a QML test override.
        if (qEnvironmentVariable("HEADROOM_EXPECT_PLATFORM_REDUCED_MOTION") != "1")
            QSKIP("Native reduced-motion fixture was not requested.");
        QVERIFY(QGuiApplication::platformName() != "offscreen");
        QTRY_COMPARE_WITH_TIMEOUT(QGuiApplication::styleHints()->accessibility()->motionPreference(),
            Qt::MotionPreference::ReducedMotion, 10000);
        QVERIFY(TrayAttention::platformReducedMotion());
        TrayAttention attention([] { return TrayAttention::platformReducedMotion(); });
        TrayVisual::Model critical;
        critical.kind = TrayVisual::Kind::Usage;
        critical.provider = "Claude";
        critical.level = Usage::WarningLevel::Critical;
        attention.update(critical);
        QCOMPARE(attention.frame().fire, 0.0);
        QVERIFY(!attention.frame().flash);

        QTemporaryDir dir;
        ControllerFixture controller(dir.filePath("settings.json"), TestUsage::snapshot());
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo; UpdateService updateService(false); RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", true);
        engine.rootContext()->setContextProperty("startHidden", true);
        engine.rootContext()->setContextProperty("captureMode", false);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        auto theme = QQmlExpression(qmlContext(window), window, "Theme").evaluate().value<QObject *>(); QVERIFY(theme);
        QCOMPARE(theme->property("reducedMotionOverride").toBool(), false);
        QTRY_VERIFY(theme->property("reducedMotion").toBool());
        window->show();
        QVERIFY(QTest::qWaitForWindowExposed(window));

        // When: the real loading indicator is visible during a refresh.
        auto state = controller.state(); state["loading"] = true;
        QVERIFY(window->setProperty("state", state));
        auto indicator = window->findChild<QQuickItem *>("refreshIndicator"); QVERIFY(indicator);
        // Then: SVG loaded successfully, but neither QML nor tray animates.
        QTRY_VERIFY(indicator->isVisible());
        QTRY_COMPARE(indicator->property("status").toInt(), 1); // Image.Ready
        QVERIFY(!indicator->property("playing").toBool());
    }
};

int main(int argc, char **argv) {
    QQuickStyle::setStyle("Basic");
    QApplication app(argc, argv);
    app.setPalette(headroomPalette());
    app.setApplicationVersion(HEADROOM_VERSION);
    PlatformMotionTest test;
    return QTest::qExec(&test, argc, argv);
}
#include "test_platformmotion.moc"
