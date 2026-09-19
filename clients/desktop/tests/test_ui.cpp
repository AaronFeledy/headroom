#include "usagefixture.h"
#include "controller.h"
#include "usage.h"
#include "startup.h"
#include "appinfo.h"
#include "popup.h"
#include "updateservice.h"
#include "remoteupdate.h"
#include "palette.h"
#include <QApplication>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQmlComponent>
#include <QQmlProperty>
#include <QQuickWindow>
#include <QQuickStyle>
#include <QQuickItem>
#include <QDesktopServices>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTemporaryDir>
#include <QElapsedTimer>
#include <QtTest>
#include <cmath>

QQuickItem *findItem(QQuickItem *root, const QString &name) {
    if (root->objectName() == name) return root;
    for (auto child : root->childItems()) if (auto found = findItem(child, name)) return found;
    return nullptr;
}
void escapeFocusedItem(QQuickItem *item) {
    QVERIFY(item);
    auto *window = item->window();
    QVERIFY(window);
    item->forceActiveFocus();
    QTRY_COMPARE(window->activeFocusItem(), item);
    QTest::keyClick(window, Qt::Key_Escape);
}
class UrlCapture : public QObject {
    Q_OBJECT
public:
    QList<QUrl> urls;
public slots:
    void capture(const QUrl &url) { urls.append(url); }
};
class UiTest : public QObject {
    Q_OBJECT
private slots:
    void notificationAttentionIsConsumedOnOpen_data() {
        QTest::addColumn<QSize>("size");
        QTest::newRow("compact") << QSize(460, 600);
        QTest::newRow("wide") << QSize(1180, 940);
    }
    void notificationAttentionIsConsumedOnOpen() {
        QFETCH(QSize, size);
        QTemporaryDir dir;
        ControllerFixture controller(dir.filePath("settings.json"));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo;
        UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", true);
        engine.rootContext()->setContextProperty("startHidden", true);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        window->resize(size);
        QTRY_COMPARE(controller.providers().size(), 4);
        controller.notifications()->present(); controller.notifications()->endPresentation();
        window->setProperty("filter", "Claude");
        controller.notifications()->post("meter_Claude_session", "Claude · Session · Warning", "Another synthetic warning", 2);
        controller.notifications()->post("meter_Cursor_api", "Cursor · Other Models · Warning", "Synthetic usage warning", 2);
        QCOMPARE(controller.notifications()->unreadCount(), 2);
        QTest::qWait(80);
        QCOMPARE(controller.notifications()->unreadCount(), 2); // Hidden windows cannot consume.
        window->show(); window->requestActivate();
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QTRY_COMPARE(controller.notifications()->unreadCount(), 0);
        QCOMPARE(window->property("filter").toString(), "All providers");
        auto highlight = findItem(window->contentItem(), "notificationHighlight_meter_Cursor_api");
        QVERIFY(highlight);
        QTRY_VERIFY(highlight->property("flashing").toBool());
        auto scroll = findItem(window->contentItem(), "meterScroll"); QVERIFY(scroll);
        QTRY_VERIFY(([&] {
            const auto point = highlight->mapToItem(scroll, QPointF(highlight->width()/2, highlight->height()/2));
            return point.y() >= 0 && point.y() <= scroll->height();
        })());
        auto summary = findItem(window->contentItem(), "notificationSummaryLink");
        QVERIFY(summary); QVERIFY(summary->isVisible());
        const QString capture = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
        if (!capture.isEmpty()) {
            QDir().mkpath(capture); QTest::qWait(300);
            QVERIFY(window->grabWindow().save(QDir(capture).filePath(QString("notification-%1.png").arg(size.width()))));
            QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
                summary->mapToScene(QPointF(summary->width()/2, summary->height()/2)).toPoint());
            QTest::qWait(100);
            QVERIFY(window->grabWindow().save(QDir(capture).filePath(QString("notification-summary-%1.png").arg(size.width()))));
        }
        if (size.width() < 700) {
            auto earlier = findItem(window->contentItem(), "notificationHighlight_meter_Claude_session");
            QVERIFY(earlier);
            QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
                summary->mapToScene(QPointF(summary->width()/2, summary->height()/2)).toPoint());
            QTRY_VERIFY(earlier->property("flashing").toBool());
        }
        window->hide();
        QVERIFY(controller.notifications()->presented().isEmpty());
        window->show(); window->requestActivate();
        QTest::qWait(100);
        QVERIFY(!highlight->property("flashing").toBool());
        QVERIFY(!summary->isVisible());
        // Summary navigation restores a filter changed after the notification arrived.
        window->setProperty("filter", "Claude");
        const QVariant event = QVariantMap{{"target", "meter_Cursor_api"}};
        QVERIFY(QMetaObject::invokeMethod(window, "revealNotification", Q_ARG(QVariant, event), Q_ARG(QVariant, true)));
        QTRY_COMPARE(window->property("filter").toString(), "All providers");
        QTest::qWait(80);
        // A new event while looking gets attention immediately, without a stale dot.
        controller.notifications()->post("meter_Claude_session", "Claude · Session · Critical", "Synthetic escalation", 3);
        QTRY_COMPARE(controller.notifications()->unreadCount(), 0);
        auto live = findItem(window->contentItem(), "notificationHighlight_meter_Claude_session");
        QVERIFY(live); QTRY_VERIFY(live->property("flashing").toBool());
        // An overlay postpones acknowledgement until the dashboard is actually shown.
        auto settings = window->findChild<QObject *>("settingsPanel"); QVERIFY(settings);
        QVERIFY(QMetaObject::invokeMethod(settings, "open"));
        QTRY_VERIFY(settings->property("opened").toBool());
        controller.notifications()->post("meter_Codex_weekly", "ChatGPT · Weekly · Warning", "Synthetic warning", 2);
        QTest::qWait(80);
        QCOMPARE(controller.notifications()->unreadCount(), 1);
        QVERIFY(QMetaObject::invokeMethod(settings, "close"));
        QTRY_COMPARE(controller.notifications()->unreadCount(), 0);
        // Historical event survives a disappeared target without blocking acknowledgement.
        window->hide();
        controller.notifications()->post("meter_Gone_missing", "Removed meter", "Previous usage event", 2);
        window->show(); window->requestActivate();
        QTRY_COMPARE(controller.notifications()->unreadCount(), 0);
        QCOMPARE(controller.notifications()->presented().size(), 1);
        window->hide();
    }
    void dragReordersAndDrivesTray() {
        QTemporaryDir dir;
        const auto capture = [&](const QString &name) {
            const QString requested = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
            if (!requested.isEmpty()) { QDir().mkpath(requested); return QDir(requested).filePath(name); }
            return dir.filePath(name);
        };
        CredentialServiceOptions credentialOptions; credentialOptions.enabled = false;
        ControllerFixture controller(dir.filePath("settings.json"));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo;
        UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", true);
        engine.rootContext()->setContextProperty("startHidden", true);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
        QVERIFY(window);
        TrayPopup popup(window, true);
        popup.show();
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QCOMPARE(QQuickStyle::name(), QString("Basic"));
        QVERIFY(window->flags().testFlag(Qt::FramelessWindowHint));
        QVERIFY(!window->flags().testFlag(Qt::WindowMinMaxButtonsHint));
        QTRY_COMPARE(controller.providers().size(), 4);
        auto marker = findItem(window->contentItem(), "paceMarker_Claude_session");
        auto label = findItem(window->contentItem(), "paceLabel_Claude_session");
        QVERIFY(marker); QVERIFY(marker->isVisible()); QVERIFY(label);
        QCOMPARE(marker->property("color").value<QColor>(), QColor("#f8f8f2"));
        QVERIFY(label->property("text").toString().contains("under pace"));
        const double markerFraction = (marker->x() + marker->width() / 2) / marker->parentItem()->width();
        QVERIFY(std::abs(markerFraction - (1.0 - 8400.0 / 18000)) < 0.01);
        auto verifyNotches = [&]() {
            struct Scale { QString name; int count; double step; };
            for (const auto &scale : {Scale{"Claude_session", 4, 1.0 / 5},
                    Scale{"Claude_weekly", 6, 1.0 / 7}, Scale{"Cursor_auto", 4, 7.0 / 30}}) {
                QTRY_VERIFY_WITH_TIMEOUT(findItem(window->contentItem(), "meter_" + scale.name), 1000);
                auto scaleMeter = findItem(window->contentItem(), "meter_" + scale.name); QVERIFY(scaleMeter);
                const auto ticks = controller.notches(scale.name.section('_', 0, 0), scaleMeter->property("bucket").toMap());
                QCOMPARE(ticks.size(), scale.count);
                for (int i = 0; i < scale.count; ++i) {
                    QVERIFY(std::abs(ticks[i].toMap()["fraction"].toDouble() - scale.step * (i + 1)) < 0.000001);
                }
            }
        };
        verifyNotches();
        auto cursorLabel = findItem(window->contentItem(), "meterLabel_Cursor_auto");
        QVERIFY(cursorLabel); QCOMPARE(cursorLabel->property("text").toString(), QString("Cursor Models"));
        QVERIFY(findItem(window->contentItem(), "meter_Claude_weekly_fable"));
        QVERIFY(findItem(window->contentItem(), "meter_Cursor_weekly_grok_bot"));
        auto statusOnlyGraph = findItem(window->contentItem(), "usageGraph_Cursor_on_demand");
        QVERIFY(statusOnlyGraph); QVERIFY(!statusOnlyGraph->isVisible());
        auto statusOnlyText = findItem(window->contentItem(), "meterStatus_Cursor_on_demand");
        QVERIFY(statusOnlyText); QCOMPARE(statusOnlyText->property("text").toString(), QString("On-demand enabled"));
        auto measuredStatus = findItem(window->contentItem(), "meterStatus_Cursor_api");
        auto measuredReset = findItem(window->contentItem(), "meterReset_Cursor_api");
        QVERIFY(measuredStatus); QVERIFY(measuredStatus->isVisible()); QVERIFY(measuredReset); QVERIFY(!measuredReset->isVisible());
        QVERIFY(!findItem(window->contentItem(), "meter_Grok_session"));
        const auto claudeCard = findItem(window->contentItem(), "providerCard_Claude");
        const auto grokCard = findItem(window->contentItem(), "providerCard_Grok");
        QVERIFY(claudeCard); QVERIFY(grokCard);
        auto hover = claudeCard->findChild<QObject *>("providerHover_Claude"); QVERIFY(hover);
        auto track = findItem(window->contentItem(), "meterTrack_Claude_session"); QVERIFY(track);
        auto fill = findItem(window->contentItem(), "meterFill_Claude_session"); QVERIFY(fill);
        const auto restingColor = claudeCard->property("color").value<QColor>();
        QTest::mouseMove(window, track->mapToScene(QPointF(track->width() / 2, 3)).toPoint());
        if (QGuiApplication::platformName() == "offscreen") QTRY_VERIFY(hover->property("hovered").toBool());
        QCOMPARE(claudeCard->property("color").value<QColor>(), restingColor);
        QVERIFY(track->property("color").value<QColor>() != restingColor);
        QCOMPARE(fill->property("color").value<QColor>(), QColor("#bd93f9"));
        QVERIFY(window->grabWindow().save(capture("headroom-hover.png")));
        auto meter = findItem(window->contentItem(), "meter_Claude_session"); QVERIFY(meter);
        const auto originalBucket = meter->property("bucket").toMap();
        const auto originalConcern = meter->property("concern").toMap();
        for (double used : {52.0, 60.0, 68.0, 80.0, 100.0}) {
            auto bucket = originalBucket; bucket["utilization"] = used;
            bucket["resets_at"] = QDateTime::currentDateTimeUtc().addSecs(9000).toString(Qt::ISODate);
            QVERIFY(meter->setProperty("bucket", bucket));
            QVERIFY(meter->setProperty("concern", Usage::concern("Claude", bucket)));
            const QColor expected(used >= 80 ? "#ff5555" : used >= 68 ? "#ffb86c" : used >= 60 ? "#f1fa8c" : "#bd93f9");
            QTRY_COMPARE(fill->property("color").value<QColor>(), expected);
            auto title = findItem(window->contentItem(), "meterLabel_Claude_session"); QVERIFY(title);
            auto severity = findItem(window->contentItem(), "meterSeverity_Claude_session"); QVERIFY(severity);
            if (severity->isVisible()) {
                QTRY_VERIFY(!title->property("truncated").toBool());
                QTRY_VERIFY(std::abs(severity->x() - title->x() - title->width() - 8) < 1);
            }
        }
        QTRY_COMPARE(fill->width(), track->width());
        QVERIFY(window->grabWindow().save(capture("headroom-critical.png")));
        QVERIFY(meter->setProperty("bucket", originalBucket));
        QVERIFY(meter->setProperty("concern", originalConcern));
        auto rows = findItem(window->contentItem(), "providerRows"); QVERIFY(rows);
        QQuickItem *previous = nullptr;
        for (const auto &name : {"Claude", "Codex", "Cursor", "Grok"}) {
            auto row = findItem(window->contentItem(), "providerCard_" + QString(name));
            QVERIFY(row); QCOMPARE(row->width(), rows->width());
            if (previous) QVERIFY(std::abs(row->y() - previous->y() - previous->height() - 12) < 1);
            previous = row;
        }
        auto chatgpt = findItem(window->contentItem(), "providerLabel_Codex");
        QVERIFY(chatgpt); QCOMPARE(chatgpt->property("text").toString(), QString("ChatGPT"));
        // Drag from a meter, rather than a dedicated handle or header.
        auto source = findItem(window->contentItem(), "meterTrack_Codex_weekly");
        QVERIFY(source);
        const QPoint from = source->mapToScene(QPointF(source->width() / 2, 3)).toPoint();
        const QPoint to = claudeCard->mapToScene(QPointF(claudeCard->width() / 2, 20)).toPoint();
        QTest::mousePress(window, Qt::LeftButton, Qt::NoModifier, from);
        for (int i = 1; i <= 20; ++i) QTest::mouseMove(window, from + (to - from) * i / 20, 15);
        QTest::mouseRelease(window, Qt::LeftButton, Qt::NoModifier, to);
        QTRY_COMPARE(controller.primary(), QString("Codex"));
        QCOMPARE(controller.providers()[0].toMap()["provider_name"].toString(), QString("Codex"));
        QVERIFY(window->grabWindow().save(capture("headroom-reordered.png")));
        // The icon and title both offer the ordering menu; meter right-clicks do not.
        auto header = findItem(window->contentItem(), "providerHeader_Codex"); QVERIFY(header);
        auto orderMenu = header->findChild<QObject *>("providerMenu_Codex"); QVERIFY(orderMenu);
        for (const auto &part : {"providerIcon_Codex", "providerLabel_Codex"}) {
            auto headerPart = findItem(window->contentItem(), part); QVERIFY(headerPart);
            QTest::mouseClick(window, Qt::RightButton, Qt::NoModifier,
                              headerPart->mapToScene(QPointF(headerPart->width() / 2, headerPart->height() / 2)).toPoint());
            QTRY_VERIFY(orderMenu->property("opened").toBool());
            QVERIFY(QMetaObject::invokeMethod(orderMenu, "close"));
            QTRY_VERIFY(!orderMenu->property("opened").toBool());
        }
        // Reordering recreates the provider delegates.
        source = findItem(window->contentItem(), "meterTrack_Codex_weekly"); QVERIFY(source);
        QTest::mouseClick(window, Qt::RightButton, Qt::NoModifier,
                          source->mapToScene(QPointF(source->width() / 2, 3)).toPoint());
        QVERIFY(!orderMenu->property("opened").toBool());
        auto filterButton = findItem(window->contentItem(), "providerFilter"); QVERIFY(filterButton);
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
                          filterButton->mapToScene(QPointF(filterButton->width() / 2, filterButton->height() / 2)).toPoint());
        auto filterMenu = window->findChild<QObject *>("providerFilterMenu"); QVERIFY(filterMenu);
        QTRY_VERIFY(filterMenu->property("opened").toBool());
        QTest::qWait(200); QVERIFY(window->isVisible());
        QVERIFY(QMetaObject::invokeMethod(filterMenu, "close"));
        QVERIFY(window->setProperty("filter", QStringLiteral("All providers")));
        window->resize(420, 800);
        QTest::qWait(100);
        auto panel = window->findChild<QObject *>("settingsPanel"); QVERIFY(panel);
        QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTest::qWait(150);
        auto save = findItem(window->contentItem(), "saveConnection"); QVERIFY(save);
        QVERIFY(save->isVisible());
        auto localMode = findItem(window->contentItem(), "localMode");
        auto remoteMode = findItem(window->contentItem(), "remoteMode");
        auto sshMode = findItem(window->contentItem(), "sshMode");
        QVERIFY(localMode); QVERIFY(remoteMode); QVERIFY(sshMode);
        const bool startsLocal = controller.settings()["mode"].toString() == QStringLiteral("local");
        QVERIFY(startsLocal);
        QCOMPARE(localMode->property("checked").toBool(), startsLocal);
        QCOMPARE(remoteMode->property("checked").toBool(), !startsLocal);
        QCOMPARE(localMode->property("text").toString(), QStringLiteral("Local"));
        QCOMPARE(sshMode->property("text").toString(), QStringLiteral("SSH · Recommended"));
        QCOMPARE(remoteMode->property("text").toString(), QStringLiteral("HTTP(S)"));
        auto connectionModeFlow = findItem(window->contentItem(), "connectionModeFlow"); QVERIFY(connectionModeFlow);
        for (auto mode : {localMode, sshMode, remoteMode}) {
            QVERIFY(mode->x() >= 0);
            QVERIFY2(mode->x() + mode->width() <= connectionModeFlow->width() + 1,
                     qPrintable(QStringLiteral("%1 exceeds the %2px connection chooser")
                         .arg(mode->property("text").toString()).arg(connectionModeFlow->width())));
        }
        QVERIFY(sshMode->y() < remoteMode->y() || (sshMode->y() == remoteMode->y() && sshMode->x() < remoteMode->x()));
        auto backendUrl = findItem(window->contentItem(), "backendUrl"); QVERIFY(backendUrl);
        auto sshUrl = findItem(window->contentItem(), "sshUrl"); QVERIFY(sshUrl);
        QVERIFY(!sshUrl->isVisible());
        QCOMPARE(backendUrl->isVisible(), !startsLocal);
        if (startsLocal) {
            QVERIFY(remoteMode->setProperty("checked", true));
            QTRY_VERIFY(!localMode->property("checked").toBool());
            QTRY_VERIFY(backendUrl->isVisible());
        }
        QVERIFY(localMode->setProperty("checked", true)); QTRY_VERIFY(!remoteMode->property("checked").toBool());
        QTRY_VERIFY(!backendUrl->isVisible());
        QVERIFY(sshMode->setProperty("checked", true));
        QTRY_VERIFY(sshUrl->isVisible()); QTRY_VERIFY(!backendUrl->isVisible());
        QVERIFY(sshUrl->setProperty("text", QStringLiteral("ssh://user@example.test:2222")));
        QVERIFY(window->grabWindow().save(capture("headroom-settings-ssh.png")));
        QVERIFY(localMode->setProperty("checked", true));
        QTRY_VERIFY(!sshUrl->isVisible());
        QVERIFY(window->grabWindow().save(capture("headroom-settings.png")));
        auto settingsScroll = window->findChild<QObject *>("settingsScroll"); QVERIFY(settingsScroll);
        auto flickable = settingsScroll->property("contentItem").value<QObject *>(); QVERIFY(flickable);
        QVERIFY(flickable->setProperty("contentY", flickable->property("contentHeight").toDouble() - flickable->property("height").toDouble()));
        QTest::qWait(100);
        QVERIFY(window->grabWindow().save(capture("headroom-settings-lower.png")));
        QVERIFY(QMetaObject::invokeMethod(panel, "saveAndConnect"));
        QTRY_VERIFY(!panel->property("opened").toBool());
        QCOMPARE(controller.settings()["interval"].toInt(), 60);
        auto diagnostics = window->findChild<QObject *>("diagnosticsPanel"); QVERIFY(diagnostics);
        QVERIFY(QMetaObject::invokeMethod(diagnostics, "open")); QTest::qWait(100);
        auto log = findItem(window->contentItem(), "diagnosticLog"); QVERIFY(log);
        QVERIFY(log->property("text").toString().contains("Usage monitor started"));
        QVERIFY(window->grabWindow().save(capture("headroom-diagnostics.png")));
        controller.clearDiagnostics(); QTRY_COMPARE(log->property("text").toString(), QString());
        QVERIFY(QMetaObject::invokeMethod(diagnostics, "close"));
        for (int width : {820, 460, 630, 460}) {
            window->resize(width, 800); QTest::qWait(100);
            QQuickItem *prior = nullptr;
            for (const auto &value : controller.providers()) {
                auto provider = value.toMap();
                auto name = provider["provider_name"].toString();
                auto row = findItem(window->contentItem(), "providerCard_" + name); QVERIFY(row);
                QTRY_COMPARE(row->width(), rows->width());
                if (prior) QVERIFY(std::abs(row->y() - prior->y() - prior->height() - 12) < 1);
                bool firstMeter = true;
                for (const auto &bucket : provider["buckets"].toList()) {
                    const auto bucketId = bucket.toMap()["id"].toString();
                    auto meter = findItem(window->contentItem(), "meter_" + name + "_" + bucketId);
                    QVERIFY(meter);
                    QCOMPARE(meter->property("accent").value<QColor>(), QColor("#bd93f9"));
                    auto title = findItem(window->contentItem(), "meterLabel_" + name + "_" + bucketId); QVERIFY(title);
                    auto severity = findItem(window->contentItem(), "meterSeverity_" + name + "_" + bucketId); QVERIFY(severity);
                    const qreal titleSpace = std::ceil(title->implicitWidth()) + 8
                        + (severity->isVisible() ? std::ceil(severity->implicitWidth()) + 8 : 0);
                    // Long labels may elide, but spare space must not be lost to rounding.
                    if (titleSpace <= title->parentItem()->width())
                        QTRY_VERIFY2(!title->property("truncated").toBool(), qPrintable(title->objectName()));
                    if (firstMeter) QCOMPARE(meter->width(), meter->parentItem()->width());
                    firstMeter = false;
                    // Native resize/layout delivery can take more than a frame.
                    // Keep the same bounds requirement while awaiting that pass.
                    QTRY_VERIFY(meter->mapToItem(row, QPointF(0, 0)).x() + meter->width() <= row->width() + 1);
                    const auto origin = meter->mapToItem(row, QPointF(0, 0));
                    const QString geometry = QString(
                        "%1/%2 at window %3: origin=(%4,%5), meter=%6x%7, row=%8x%9")
                        .arg(name, bucketId).arg(width).arg(origin.x()).arg(origin.y())
                        .arg(meter->width()).arg(meter->height()).arg(row->width()).arg(row->height());
                    QVERIFY2(origin.x() >= 0, qPrintable(geometry));
                    QVERIFY2(origin.y() >= 0, qPrintable(geometry));
                    QVERIFY2(origin.x() + meter->width() <= row->width() + 1, qPrintable(geometry));
                    QVERIFY2(origin.y() + meter->height() <= row->height() + 1, qPrintable(geometry));
                }
                prior = row;
            }
            QVERIFY(window->grabWindow().save(capture(width == 460 ? "headroom-compact.png" : "headroom-medium.png")));
        }
        // At minimum width, dragging the panel must win over the scroll view.
        // An ordinary click on the same surface must not change the order.
        auto narrowSource = findItem(window->contentItem(), "providerCard_Claude"); QVERIFY(narrowSource);
        auto narrowTarget = findItem(window->contentItem(), "providerCard_Codex"); QVERIFY(narrowTarget);
        const QPoint narrowFrom = narrowSource->mapToScene(QPointF(narrowSource->width() - 8, 12)).toPoint();
        const QPoint narrowTo = narrowTarget->mapToScene(QPointF(narrowTarget->width() / 2, 20)).toPoint();
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier, narrowFrom);
        QCOMPARE(controller.primary(), QString("Codex"));
        QTest::mousePress(window, Qt::LeftButton, Qt::NoModifier, narrowFrom);
        for (int i = 1; i <= 20; ++i) QTest::mouseMove(window, narrowFrom + (narrowTo - narrowFrom) * i / 20, 15);
        QTest::mouseRelease(window, Qt::LeftButton, Qt::NoModifier, narrowTo);
        QTRY_COMPARE(controller.primary(), QString("Claude"));
        controller.setPrimary("Codex");
        QTRY_COMPARE(controller.primary(), QString("Codex"));
        const auto connectedState = controller.state();
        const auto retainedProviders = controller.providers();
        auto disconnectedState = connectedState;
        disconnectedState["status"] = "offline"; disconnectedState["errorKind"] = "network";
        disconnectedState["message"] = "Cannot reach your backend.";
        QVERIFY(window->setProperty("state", disconnectedState));
        auto footerBorder = findItem(window->contentItem(), "footerBorder"); QVERIFY(footerBorder);
        auto placeholder = findItem(window->contentItem(), "connectionPlaceholder"); QVERIFY(placeholder);
        for (const auto &value : retainedProviders) {
            auto card = findItem(window->contentItem(), "providerCard_" + value.toMap()["provider_name"].toString()); QVERIFY(card);
            QTRY_COMPARE(QQmlProperty(card, "border.color").read().value<QColor>(), QColor("#ff5555"));
        }
        QTRY_COMPARE(footerBorder->property("color").value<QColor>(), QColor("#ff5555"));
        QCOMPARE(controller.providers(), retainedProviders);
        auto firstCard = findItem(window->contentItem(), "providerCard_Codex"); QVERIFY(firstCard);
        QTest::mouseMove(window, firstCard->mapToScene(QPointF(12, 12)).toPoint());
        QTest::qWait(50);
        QCOMPARE(QQmlProperty(firstCard, "border.color").read().value<QColor>(), QColor("#ff5555"));
        QVERIFY(window->grabWindow().save(capture("headroom-offline.png")));
        disconnectedState["loading"] = true;
        QVERIFY(window->setProperty("state", disconnectedState));
        QCOMPARE(QQmlProperty(firstCard, "border.color").read().value<QColor>(), QColor("#ff5555"));
        QVERIFY(window->setProperty("state", connectedState));
        QTRY_VERIFY(QQmlProperty(firstCard, "border.color").read().value<QColor>() != QColor("#ff5555"));
        QTRY_COMPARE(footerBorder->property("color").value<QColor>(), QColor("#44475a"));
        // A first connection failure has no cached cards, but its panel must still warn.
        QVERIFY(window->setProperty("providers", QVariantList{}));
        disconnectedState["lastGood"] = 0; disconnectedState["loading"] = false;
        QVERIFY(window->setProperty("state", disconnectedState));
        QTRY_VERIFY(placeholder->isVisible());
        QCOMPARE(QQmlProperty(placeholder, "border.color").read().value<QColor>(), QColor("#ff5555"));
        QVERIFY(window->grabWindow().save(capture("headroom-offline-empty.png")));
        QVERIFY(window->setProperty("state", connectedState));
        QTRY_COMPARE(QQmlProperty(placeholder, "border.color").read().value<QColor>(), QColor("#44475a"));
    }
    void bankedResetsFollowWeeklyCriticalState() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const auto capture = [&](const QString &name) {
            const QString requested = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
            if (!requested.isEmpty()) { QDir().mkpath(requested); return QDir(requested).filePath(name); }
            return dir.filePath(name);
        };
        ControllerFixture controller(dir.filePath("settings.json"));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo; UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", false);
        engine.rootContext()->setContextProperty("startHidden", false);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QTRY_COMPARE(controller.providers().size(), 4);

        auto resetLabel = [&] {
            return findItem(window->contentItem(), "bankedResets_Codex");
        };
        auto label = resetLabel(); QVERIFY(label);
        QTRY_VERIFY(label->isVisible());
        QCOMPARE(label->property("text").toString(), QString("3 banked resets"));
        const QColor muted = label->property("color").value<QColor>();
        QVERIFY(muted != QColor("#ff5555"));
        QVERIFY(window->grabWindow().save(capture("headroom-banked-resets.png")));

        const QString halfWeekReset = QDateTime::currentDateTimeUtc().addSecs(7 * 86400 / 2).toString(Qt::ISODate);
        controller.replaceSnapshot(TestUsage::snapshotWithCodex(0, 41, 0, halfWeekReset));
        label = resetLabel(); QVERIFY(label); QTRY_VERIFY(!label->isVisible());

        auto missing = QJsonDocument::fromJson(TestUsage::snapshotWithCodex(0, 41, 3, halfWeekReset)).array();
        auto missingCodex = missing[1].toObject(); missingCodex.remove("rate_limit_reset_credits"); missing[1] = missingCodex;
        controller.replaceSnapshot(QJsonDocument(missing).toJson());
        label = resetLabel(); QVERIFY(label); QTRY_VERIFY(!label->isVisible());

        auto failed = QJsonDocument::fromJson(TestUsage::snapshotWithCodex(0, 41, 3, halfWeekReset)).array();
        auto failedCodex = failed[1].toObject(); failedCodex["error"] = "Unavailable"; failedCodex["is_success"] = false; failed[1] = failedCodex;
        controller.replaceSnapshot(QJsonDocument(failed).toJson());
        // Failed providers have no meters, so the weekly footer is removed too.
        QTRY_VERIFY(!resetLabel() || !resetLabel()->isVisible());

        controller.replaceSnapshot(TestUsage::snapshotWithCodex(0, 41, 1, halfWeekReset));
        label = resetLabel(); QVERIFY(label); QTRY_VERIFY(label->isVisible());
        QCOMPARE(label->property("text").toString(), QString("1 banked reset"));
        QCOMPARE(label->property("color").value<QColor>(), muted);

        for (const auto &[weekly, severity] : QList<QPair<int, int>>{{56, 1}, {68, 2}, {80, 3}, {73, 3}, {68, 2}}) {
            controller.replaceSnapshot(TestUsage::snapshotWithCodex(0, weekly, 3, halfWeekReset));
            label = resetLabel(); QVERIFY(label);
            QTRY_COMPARE(controller.concern("Codex", QVariantMap{{"id", "weekly"}})["severity"].toInt(), severity);
            const QColor expected = severity == 3 ? QColor("#ff5555") : muted;
            QTRY_COMPARE(label->property("color").value<QColor>(), expected);
            QCOMPARE(label->property("text").toString(), QString("3 banked resets"));
            if (severity == 3 && weekly == 80)
                QVERIFY(window->grabWindow().save(capture("headroom-banked-resets-critical.png")));
        }

        controller.replaceSnapshot(TestUsage::snapshotWithCodex(90, 41, 3, halfWeekReset));
        label = resetLabel(); QVERIFY(label);
        QTRY_COMPARE(controller.concern("Codex", QVariantMap{{"id", "session"}})["severity"].toInt(), 3);
        QTRY_COMPARE(controller.concern("Codex", QVariantMap{{"id", "weekly"}})["severity"].toInt(), 0);
        QTRY_COMPARE(label->property("color").value<QColor>(), muted);

        UrlCapture opened;
        QDesktopServices::setUrlHandler("https", &opened, "capture");
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
                          label->mapToScene(QPointF(label->width() / 2, label->height() / 2)).toPoint());
        QVERIFY(label->property("activeFocusOnTab").toBool());
        label->forceActiveFocus(); QTRY_VERIFY(label->hasActiveFocus());
        QTest::keyClick(window, Qt::Key_Return);
        QTest::keyClick(window, Qt::Key_Space);
        QDesktopServices::unsetUrlHandler("https");
        QCOMPARE(opened.urls, QList<QUrl>({QUrl("https://chatgpt.com/codex/settings/usage"),
            QUrl("https://chatgpt.com/codex/settings/usage"), QUrl("https://chatgpt.com/codex/settings/usage")}));
    }
    void preservesAndDisplaysCustomInterval() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        QFile settings(dir.filePath("settings.json")); QVERIFY(settings.open(QIODevice::WriteOnly));
        const QByteArray savedSettings = QByteArrayLiteral("{\"schemaVersion\":1,\"connectionMode\":\"ssh\",\"url\":\"\",\"token\":\"\",\"sshUrl\":\"ssh://saved.example.test\",\"interval\":900,\"notifications\":true,\"primary\":\"Claude\",\"order\":[\"Claude\",\"Codex\",\"Cursor\",\"Grok\"]}");
        QVERIFY(settings.write(savedSettings) > 0);
        settings.close();
        CredentialServiceOptions credentials; credentials.enabled = false;
        ControllerFixture controller(settings.fileName());
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo; UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", false);
        engine.rootContext()->setContextProperty("startHidden", false);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty()); auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        QVERIFY(QTest::qWaitForWindowExposed(window));
        auto panel = window->findChild<QObject *>("settingsPanel"); QVERIFY(panel); QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTRY_VERIFY(panel->property("opened").toBool()); QCOMPARE(panel->property("selectedInterval").toInt(), 900);
        auto interval = findItem(window->contentItem(), "refreshInterval"); QVERIFY(interval);
        QCOMPARE(interval->property("displayText").toString(), QString("15 minutes"));
        auto sshMode = findItem(window->contentItem(), "sshMode"); QVERIFY(sshMode);
        auto sshUrl = findItem(window->contentItem(), "sshUrl"); QVERIFY(sshUrl);
        QVERIFY(sshMode->property("checked").toBool());
        QCOMPARE(sshUrl->property("text").toString(), QStringLiteral("ssh://saved.example.test"));
        QVERIFY(sshUrl->isVisible());
        auto save = findItem(window->contentItem(), "saveConnection"); QVERIFY(save);
        QVERIFY(QMetaObject::invokeMethod(panel, "saveAndConnect"));
        QCOMPARE(controller.settings()["interval"].toInt(), 900);
        QVERIFY(QMetaObject::invokeMethod(panel, "close"));
    }
    void escapeClosesOverlayThenHidesToTray() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        CredentialServiceOptions credentials; credentials.enabled = false;
        ControllerFixture controller(dir.filePath("settings.json"));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo; UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", true);
        engine.rootContext()->setContextProperty("startHidden", false);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        TrayPopup popup(window, true);
        popup.show();
        window->requestActivate();
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QTRY_VERIFY(window->isVisible());
        QVERIFY(!window->findChild<QObject *>("escapeShortcut"));
        QTRY_COMPARE(controller.providers().size(), 4);

        auto filterButton = findItem(window->contentItem(), "providerFilter"); QVERIFY(filterButton);
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
                          filterButton->mapToScene(QPointF(filterButton->width() / 2, filterButton->height() / 2)).toPoint());
        auto filterMenu = window->findChild<QObject *>("providerFilterMenu"); QVERIFY(filterMenu);
        QTRY_VERIFY(filterMenu->property("opened").toBool());
        QTest::keyClick(window, Qt::Key_Escape);
        QTRY_VERIFY(!filterMenu->property("opened").toBool());
        QVERIFY(window->isVisible());

        auto panel = window->findChild<QObject *>("settingsPanel"); QVERIFY(panel);
        QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTRY_VERIFY(panel->property("opened").toBool());
        QTest::keyClick(window, Qt::Key_Escape);
        QTRY_VERIFY(!panel->property("opened").toBool());
        QVERIFY(window->isVisible());

        QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTRY_VERIFY(panel->property("opened").toBool());
        auto sshMode = findItem(window->contentItem(), "sshMode"); QVERIFY(sshMode);
        QVERIFY(sshMode->setProperty("checked", true));
        auto sshUrl = findItem(window->contentItem(), "sshUrl"); QVERIFY(sshUrl);
        QTRY_VERIFY(sshUrl->isVisible());
        escapeFocusedItem(sshUrl);
        QTRY_VERIFY(!panel->property("opened").toBool());
        QVERIFY(window->isVisible());

        QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTRY_VERIFY(panel->property("opened").toBool());
        auto interval = findItem(window->contentItem(), "refreshInterval"); QVERIFY(interval);
        auto comboPopup = interval->property("popup").value<QObject *>(); QVERIFY(comboPopup);
        QVERIFY(QMetaObject::invokeMethod(comboPopup, "open"));
        QTRY_VERIFY(comboPopup->property("opened").toBool());
        QTest::keyClick(window, Qt::Key_Escape);
        QTRY_VERIFY(!panel->property("opened").toBool());
        QVERIFY(window->isVisible());

        auto diagnostics = window->findChild<QObject *>("diagnosticsPanel"); QVERIFY(diagnostics);
        QVERIFY(QMetaObject::invokeMethod(diagnostics, "open"));
        QTRY_VERIFY(diagnostics->property("opened").toBool());
        auto log = findItem(window->contentItem(), "diagnosticLog"); QVERIFY(log);
        QVERIFY(log->property("readOnly").toBool());
        QVERIFY(log->property("selectByMouse").toBool());
        QCOMPARE(log->property("placeholderText").toString(), QString("No events recorded."));
        QCOMPARE(log->objectName(), QString("diagnosticLog"));
        escapeFocusedItem(log);
        QTRY_VERIFY(!diagnostics->property("opened").toBool());
        QVERIFY(window->isVisible());

        window->requestActivate();
        QTRY_VERIFY(window->isActive());
        QTest::keyClick(window, Qt::Key_Escape);
        QTRY_VERIFY(!window->isVisible());
    }
    void updateIndicatorsNavigateWithoutApplying_data() {
        QTest::addColumn<QSize>("size");
        QTest::newRow("minimum") << QSize(460, 420);
        QTest::newRow("compact") << QSize(699, 600);
        QTest::newRow("wide-boundary") << QSize(700, 600);
        QTest::newRow("wide") << QSize(960, 900);
    }
    void updateIndicatorsNavigateWithoutApplying() {
        QFETCH(QSize, size);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        ControllerFixture controller(dir.filePath("settings.json"));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        QScopedPointer<QObject> services;
        QQmlApplicationEngine engine;
        // UI-only states: no real updater, remote command, or account action can run.
        QQmlComponent fixtures(&engine);
        fixtures.setData(R"(
            import QtQml
            QtObject {
                property QtObject updates: QtObject {
                    property string state: "current"
                    property string statusText: state === "staged"
                        ? "Headroom 8.4.2+test.1 is staged. Restart to apply it."
                        : "Synthetic update status"
                    property string latestVersion: "8.4.2+test.1"
                    property string updateMethod: "automatic"
                    property bool busy: state === "downloading" || state === "applying"
                    property bool canCancel: state === "downloading"
                    property bool canCheck: !busy
                    property bool canStage: state === "available"
                    property bool canRepair: false
                    property bool restartAvailable: state === "staged"
                    property int actions: 0
                    function checkForUpdates() { actions++ }
                    function stageUpdate() { actions++ }
                    function restartToApply() { actions++ }
                    function repairInstallation() { actions++ }
                    function cancel() { actions++ }
                    function openUpdateMethod() { actions++ }
                }
                property QtObject info: QtObject {
                    property string applicationVersion: "8.4.1"
                    property string serverVersion: "8.4.0"
                    property string serverStatus: "Connected"
                    property bool checkingServer: false
                    property string serverUpdateNotice: ""
                    function refreshServer() {}
                }
                property QtObject remote: QtObject {
                    property bool available: true
                    property bool busy: false
                    property bool canStart: true
                    property string state: "idle"
                    property string statusText: ""
                    property int actions: 0
                    function start() { actions++ }
                }
            }
        )", QUrl());
        services.reset(fixtures.create());
        QVERIFY2(services, qPrintable(fixtures.errorString()));
        auto updates = services->property("updates").value<QObject *>(); QVERIFY(updates);
        auto info = services->property("info").value<QObject *>(); QVERIFY(info);
        auto remote = services->property("remote").value<QObject *>(); QVERIFY(remote);
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", info);
        engine.rootContext()->setContextProperty("updateService", updates);
        engine.rootContext()->setContextProperty("remoteUpdateService", remote);
        engine.rootContext()->setContextProperty("trayAvailable", false);
        engine.rootContext()->setContextProperty("startHidden", false);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        window->resize(size);
        QVERIFY(QTest::qWaitForWindowExposed(window));
        QTRY_COMPARE(controller.providers().size(), 4);
        const auto originalSettings = controller.settings();
        auto wide = findItem(window->contentItem(), "desktopUpdateIndicator"); QVERIFY(wide);
        auto compact = findItem(window->contentItem(), "compactUpdateIndicator"); QVERIFY(compact);
        auto server = findItem(window->contentItem(), "serverUpdateIndicator"); QVERIFY(server);
        auto footer = findItem(window->contentItem(), "stickyFooter"); QVERIFY(footer);
        auto indicator = size.width() < 700 ? compact : wide;
        auto other = size.width() < 700 ? wide : compact;
        QVERIFY(!wide->isVisible()); QVERIFY(!compact->isVisible()); QVERIFY(!server->isVisible());
        for (const QString state : {"checking", "unavailable", "current"}) {
            updates->setProperty("state", state);
            QVERIFY(!wide->isVisible()); QVERIFY(!compact->isVisible());
        }
        updates->setProperty("state", "available");
        for (const QString method : {"source", "system"}) {
            updates->setProperty("updateMethod", method);
            QVERIFY(!wide->isVisible()); QVERIFY(!compact->isVisible());
        }
        updates->setProperty("updateMethod", "automatic");

        const QList<QPair<QString, QString>> states{
            {"available", "↑ Update available"}, {"downloading", "Downloading…"},
            {"staged", "↑ Restart to update"}, {"applying", "Applying update…"},
            {"failed", "Update needs attention"}};
        for (const auto &[state, label] : states) {
            updates->setProperty("state", state);
            QTRY_VERIFY(indicator->isVisible());
            QVERIFY(!other->isVisible());
            QCOMPARE(indicator->property("text").toString(), label);
            QCOMPARE(indicator->property("needsAttention").toBool(), state == "failed");
            const auto contained = [&] {
                const auto at = indicator->mapToItem(footer, QPointF());
                return at.x() >= 0 && at.y() >= 0
                    && at.x() + indicator->width() <= footer->width()
                    && at.y() + indicator->height() <= footer->height()
                    && footer->y() >= 0 && footer->y() + footer->height() <= window->height() + 1;
            };
            QTRY_VERIFY(contained());
            if (size.width() >= 700) {
                auto filter = findItem(window->contentItem(), "providerFilter"); QVERIFY(filter);
                QTRY_VERIFY(indicator->mapToScene(QPointF(indicator->width(), 0)).x()
                            <= filter->mapToScene(QPointF()).x());
            }
        }
        updates->setProperty("state", "staged");
        updates->setProperty("latestVersion", "8.4.1");
        QCOMPARE(indicator->property("text").toString(), QString("↑ Restart to apply"));
        updates->setProperty("latestVersion", "8.4.2+test.1");
        info->setProperty("serverUpdateNotice", "Your desktop is newer than the remote server (8.4.0). Use Update server in About & Updates.");
        QTRY_VERIFY(server->isVisible());
        QCOMPARE(server->property("text").toString(), QString("↑ Server update available"));
        const auto separated = [&] {
            const QRectF desktopRect(indicator->mapToScene(QPointF()), indicator->size());
            const QRectF serverRect(server->mapToScene(QPointF()), server->size());
            return !desktopRect.intersects(serverRect) && serverRect.right() <= window->width();
        };
        QTRY_VERIFY(separated());

        window->requestActivate();
        controller.notifications()->post("desktopUpdate", "Headroom is ready to restart", "Synthetic staged update");
        QTRY_COMPARE(controller.notifications()->unreadCount(), 0);
        auto updateHighlight = findItem(window->contentItem(), "notificationHighlight_" + indicator->objectName());
        QVERIFY(updateHighlight); QTRY_VERIFY(updateHighlight->property("flashing").toBool());
        auto hiddenHighlight = findItem(window->contentItem(), "notificationHighlight_" + other->objectName());
        QVERIFY(hiddenHighlight); QVERIFY(!hiddenHighlight->property("flashing").toBool());

        const QString captureDir = qEnvironmentVariable("HEADROOM_TEST_CAPTURE_DIR");
        if (!captureDir.isEmpty()) {
            QDir().mkpath(captureDir);
            QTest::qWait(100);
            QVERIFY(window->grabWindow().save(QDir(captureDir).filePath(
                QString("update-footer-%1.png").arg(size.width()))));
        }
        auto panel = window->findChild<QObject *>("settingsPanel"); QVERIFY(panel);
        // Keyboard activation opens the details; it must not install or restart.
        indicator->forceActiveFocus();
        QTest::keyClick(window, Qt::Key_Space);
        QTRY_VERIFY(panel->property("opened").toBool());
        auto heading = findItem(window->contentItem(), "updatesHeading"); QVERIFY(heading);
        auto scroll = findItem(window->contentItem(), "settingsScroll"); QVERIFY(scroll);
        QTRY_VERIFY(heading->hasActiveFocus());
        const auto headingInView = [&] {
            const auto at = heading->mapToItem(scroll, QPointF());
            return at.y() >= -1 && at.y() + heading->height() <= scroll->height() + 1;
        };
        QTRY_VERIFY(headingInView());
        auto restart = findItem(window->contentItem(), "restartToApply"); QVERIFY(restart);
        QTRY_VERIFY(restart->mapToItem(scroll, QPointF()).y() >= 0);
        QTRY_VERIFY(restart->mapToItem(scroll, QPointF(0, restart->height())).y() <= scroll->height());
        auto version = findItem(window->contentItem(), "availableUpdateVersion"); QVERIFY(version);
        QVERIFY(version->isVisible());
        QCOMPARE(version->property("text").toString(), QString("Available: Headroom 8.4.2+test.1"));
        if (!captureDir.isEmpty()) {
            QTest::qWait(100);
            QVERIFY(window->grabWindow().save(QDir(captureDir).filePath(
                QString("update-details-%1.png").arg(size.width()))));
        }
        UrlCapture capture;
        QDesktopServices::setUrlHandler("https", &capture, "capture");
        auto notes = findItem(window->contentItem(), "updateReleaseNotes"); QVERIFY(notes);
        const bool openedNotes = QMetaObject::invokeMethod(notes, "clicked");
        QDesktopServices::unsetUrlHandler("https");
        QVERIFY(openedNotes);
        QCOMPARE(capture.urls, QList<QUrl>{QUrl("https://github.com/AaronFeledy/headroom/releases/tag/v8.4.2%2Btest.1")});
        QVERIFY(QMetaObject::invokeMethod(panel, "close"));
        QTRY_VERIFY(!panel->property("opened").toBool());
        // The server notice uses the same destination and never invokes SSH itself.
        QTest::mouseClick(window, Qt::LeftButton, Qt::NoModifier,
            server->mapToScene(QPointF(server->width() / 2, server->height() / 2)).toPoint());
        QTRY_VERIFY(panel->property("opened").toBool());
        QTRY_VERIFY(heading->hasActiveFocus());
        QTRY_VERIFY(headingInView());
        QCOMPARE(updates->property("actions").toInt(), 0);
        QCOMPARE(remote->property("actions").toInt(), 0);
        QCOMPARE(controller.settings(), originalSettings);
        updates->setProperty("state", "current");
        QVERIFY(!version->isVisible()); QVERIFY(!notes->isVisible());
        QVERIFY(QMetaObject::invokeMethod(panel, "close"));
        QTRY_VERIFY(!panel->property("opened").toBool());
        QVERIFY(!wide->isVisible()); QVERIFY(!compact->isVisible());
        info->setProperty("serverUpdateNotice", "");
        QVERIFY(!server->isVisible());
        // Ordinary settings entry should still start at the connection section.
        QVERIFY(QMetaObject::invokeMethod(panel, "open"));
        QTRY_VERIFY(panel->property("opened").toBool());
        const auto flickable = scroll->property("contentItem").value<QObject *>(); QVERIFY(flickable);
        QTRY_COMPARE(flickable->property("contentY").toReal(), 0.0);
    }
    void rendersOneTwoFourAndTwelveMeters() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        QJsonArray providers;
        const QList<QPair<QString, int>> shapes{{"Claude", 1}, {"Codex", 2}, {"Cursor", 4}, {"Grok", 12}};
        for (const auto &[name, count] : shapes) {
            QJsonArray buckets;
            for (int i = 0; i < count; ++i) {
                buckets.append(QJsonObject{{"id", QString("meter_%1").arg(i)},
                    {"label", QString("Allowance 測定 %1").arg(i + 1)}, {"utilization", double((i * 7 + 13) % 101)},
                    {"resets_at", QDateTime::currentDateTimeUtc().addDays(7).toString(Qt::ISODate)},
                    {"status_text", i == count - 1 ? QJsonValue(QString("Status %1 / %2").arg(i + 1).arg(count)) : QJsonValue(QJsonValue::Null)}});
            }
            providers.append(QJsonObject{{"provider_name", name}, {"is_success", true}, {"error", QJsonValue::Null},
                {"needs_reauth", false}, {"reauth_command", QJsonValue::Null}, {"buckets", buckets}});
        }
        CredentialServiceOptions credentials; credentials.enabled = false;
        ControllerFixture controller(dir.filePath("settings.json"),
                              QJsonDocument(providers).toJson(QJsonDocument::Compact));
        StartupService startup(dir.path(), QCoreApplication::applicationFilePath(), false);
        AppInfo appInfo; UpdateService updateService(false);
        RemoteUpdateService remoteUpdate;
        QQmlApplicationEngine engine;
        engine.rootContext()->setContextProperty("backend", &controller);
        engine.rootContext()->setContextProperty("startupService", &startup);
        engine.rootContext()->setContextProperty("appInfo", &appInfo);
        engine.rootContext()->setContextProperty("updateService", &updateService);
        engine.rootContext()->setContextProperty("remoteUpdateService", &remoteUpdate);
        engine.rootContext()->setContextProperty("trayAvailable", false);
        engine.rootContext()->setContextProperty("startHidden", false);
        engine.rootContext()->setContextProperty("captureMode", true);
        engine.load(QUrl::fromLocalFile(QString(SOURCE_DIR) + "/qml/Main.qml"));
        QVERIFY(!engine.rootObjects().isEmpty());
        auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first()); QVERIFY(window);
        QVERIFY(QTest::qWaitForWindowExposed(window)); QTRY_COMPARE(controller.providers().size(), 4);
        auto rows = findItem(window->contentItem(), "providerRows");
        auto footer = findItem(window->contentItem(), "stickyFooter");
        QVERIFY(rows); QVERIFY(footer);
        for (const QSize size : {QSize(960, 900), window->minimumSize()}) {
            window->resize(size);
            QTRY_COMPARE(window->size(), size);
            QTest::qWait(100);
            const bool compact = window->width() < 700;
            QCOMPARE(window->property("compact").toBool(), compact);
            QVERIFY(window->width() >= window->minimumWidth());
            QVERIFY(window->height() >= window->minimumHeight());
            const auto footerContained = [&] { return footer->y() >= 0
                && footer->y() + footer->height() <= window->height() + 1; };
            QElapsedTimer footerSettle; footerSettle.start();
            while (!footerContained() && footerSettle.elapsed() < 5000) QTest::qWait(20);
            const QString footerGeometry = QString("window=%1x%2 content=%3x%4 footer@%5=%6")
                .arg(window->width()).arg(window->height()).arg(window->contentItem()->width())
                .arg(window->contentItem()->height()).arg(footer->y()).arg(footer->height());
            QVERIFY2(footerContained(), qPrintable(footerGeometry));
            for (const auto &[name, count] : shapes) {
                auto card = findItem(window->contentItem(), "providerCard_" + name); QVERIFY(card);
                for (int i = 0; i < count; ++i) {
                    auto meter = findItem(window->contentItem(), QString("meter_%1_meter_%2").arg(name).arg(i));
                    QVERIFY2(meter, qPrintable(QString("missing %1 meter %2").arg(name).arg(i)));
                    if (compact && i == 0) QTRY_COMPARE(meter->width(), meter->parentItem()->width());
                    const auto contained = [&] {
                        const auto at = meter->mapToItem(card, QPointF());
                        return at.x() >= -1 && at.y() >= -1
                            && at.x() + meter->width() <= card->width() + 2
                            && at.y() + meter->height() <= card->height() + 2;
                    };
                    QElapsedTimer settle; settle.start();
                    while (!contained() && settle.elapsed() < 5000) QTest::qWait(20);
                    const auto at = meter->mapToItem(card, QPointF());
                    const QString geometry = QString("%1/%2 window=%3x%4 content=%5x%6 card=%7x%8 meter@%9,%10=%11x%12")
                        .arg(name).arg(i).arg(window->width()).arg(window->height())
                        .arg(window->contentItem()->width()).arg(window->contentItem()->height())
                        .arg(card->width()).arg(card->height()).arg(at.x()).arg(at.y())
                        .arg(meter->width()).arg(meter->height());
                    QVERIFY2(contained(), qPrintable(geometry));
                }
            }
        }
    }
};
int main(int argc, char **argv) { QQuickStyle::setStyle("Basic"); QQuickWindow::setDefaultAlphaBuffer(true); QApplication app(argc, argv); app.setPalette(headroomPalette()); app.setApplicationVersion(HEADROOM_VERSION); UiTest test; return QTest::qExec(&test, argc, argv); }
#include "test_ui.moc"
