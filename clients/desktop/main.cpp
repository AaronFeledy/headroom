#include "controller.h"
#include "usage.h"
#include "trayvisual.h"
#include "trayattention.h"
#include "startup.h"
#include "appinfo.h"
#include "updateservice.h"
#include "popup.h"
#include "displayplatform.h"
#include "instance.h"
#include "palette.h"
#include <QCursor>
#include <memory>
#ifdef HEADROOM_KDE_TRAY
#include <KStatusNotifierItem>
#endif
#include <QApplication>
#include <QCommandLineParser>
#include <QFileInfo>
#include <QMenu>
#include <QMessageBox>
#include <QPainter>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickWindow>
#include <QQuickStyle>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>
#include <QSignalBlocker>
#include <QSystemTrayIcon>
#include <QJsonArray>
#include <cstdio>
#include <cstring>

namespace {
int desktopCLIRequest(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    app.setOrganizationName("Headroom"); app.setApplicationName("Headroom");
    QFile input;
    if (!input.open(stdin, QIODevice::ReadOnly)) return 1;
    const auto bytes = input.read(4097);
    if (bytes.isEmpty() || bytes.size() > 4096) return 1;
    const auto command = QJsonDocument::fromJson(bytes);
    if (!command.isObject()) return 1;
    InstanceService instance(SettingsService::defaultPath());
    const int timeout = command.object().value(QStringLiteral("command")).toString() == QStringLiteral("update") ? 8 * 60 * 1000 : 3000;
    const auto response = instance.request(command.toJson(QJsonDocument::Compact), timeout);
    if (response.isEmpty()) return instance.primaryUnavailable() ? 3 : 1;
    QFile output;
    if (!output.open(stdout, QIODevice::WriteOnly) || output.write(response) != response.size() || !output.flush()) return 1;
    return 0;
}
}

int main(int argc, char **argv) {
    // The CLI bridge is headless and only talks to an existing desktop. It must
    // never instantiate Controller, discover credentials, or start a poller.
    if (argc == 2 && std::strcmp(argv[1], "--headroom-cli-request") == 0) return desktopCLIRequest(argc, argv);
    // Select the positioning-capable display backend before Qt creates it.
    DesktopPlatform::configure();
    QQuickStyle::setStyle("Basic");
    // QML windows need an alpha buffer before creation for transparent corners.
    QQuickWindow::setDefaultAlphaBuffer(true);
    QApplication app(argc, argv);
    app.setPalette(headroomPalette());
    app.setOrganizationName("Headroom"); app.setApplicationName("Headroom"); app.setApplicationVersion(HEADROOM_VERSION);
    app.setDesktopFileName("headroom");
    app.setWindowIcon(QIcon(":/qt/qml/Headroom/headroom.svg"));
    QCommandLineParser parser; parser.setApplicationDescription("A little more room to think. Native AI usage monitor."); parser.addHelpOption(); parser.addVersionOption();
    parser.addOption({"background", "Start in the system tray."});
    parser.addOption({"screenshot", "Save a screenshot, then exit (for visual verification).", "path"});
    parser.addOption({"config", "Use an alternate settings file.", "path"});
    parser.addOption({"headroom-ready-file", "Private update readiness endpoint.", "path"});
    parser.addOption({"headroom-update-restart", "Open the popup after a verified update restart."});
    parser.addOption({"headroom-installed-restart", "Open the popup after an installer restart."});
    parser.process(app);
    const bool capture = parser.isSet("screenshot");
    const bool isolated = parser.isSet("config");
    if (isolated && parser.value("config").trimmed().isEmpty()) {
        QMessageBox::critical(nullptr, "Headroom", "The --config option requires a settings file path.");
        return 2;
    }
    const QString settingsPath = isolated ? parser.value("config") : SettingsService::defaultPath();
    InstanceService instance(settingsPath);
    if (!capture) {
        const auto result = instance.start();
        if (result == InstanceService::Result::Secondary) return 0;
        if (result == InstanceService::Result::Error) {
            QMessageBox::critical(nullptr, "Headroom", "Headroom could not communicate with the running instance.");
            return 1;
        }
    }
    Controller controller(parser.value("config"), nullptr, !capture && !isolated, {}, {}, {}, !capture);
    StartupService startup({}, {}, !capture && !isolated);
    AppInfo appInfo;
    // Version checks use the selected transport after startup, including after
    // an update restart. An SSH/HTTP address never grants local update ownership.
    QObject::connect(&appInfo, &AppInfo::backendChanged, &appInfo, &AppInfo::refreshServer, Qt::QueuedConnection);
    QTimer serverVersionTimer;
    serverVersionTimer.setInterval(5 * 60 * 1000);
    QObject::connect(&serverVersionTimer, &QTimer::timeout, &appInfo, &AppInfo::refreshServer);
    if (!capture) serverVersionTimer.start();
    UpdateServiceOptions updateOptions;
    updateOptions.diagnostic = [&controller](const QString &message) { controller.logUpdate(message); };
    UpdateService updateService(!capture && !isolated, std::move(updateOptions));
    if (!capture && !isolated) instance.setRequestHandler([&](const QByteArray &request) {
        const auto document = QJsonDocument::fromJson(request);
        if (!document.isObject()) return QByteArray();
        const auto object = document.object();
        const auto command = object.value(QStringLiteral("command")).toString();
        QJsonObject response;
        if (command == QStringLiteral("usage")) {
            response = {{"ok", true}, {"result", QJsonArray::fromVariantList(controller.providers())},
                        {"status", controller.state().value(QStringLiteral("status")).toString()},
                        {"last_good", controller.state().value(QStringLiteral("lastGood")).toLongLong()}};
        } else if (command == QStringLiteral("info")) {
            response = {{"ok", true}, {"version", QCoreApplication::applicationVersion()},
                        {"pid", qint64(QCoreApplication::applicationPid())},
                        {"executable", QFileInfo(QCoreApplication::applicationFilePath()).canonicalFilePath()},
                        {"install_root", QFileInfo(qEnvironmentVariable("HEADROOM_INSTALL_ROOT")).canonicalFilePath()}};
        } else return QByteArray();
        return QJsonDocument(response).toJson(QJsonDocument::Compact);
    });
    if (!capture && !isolated) instance.setAsyncRequestHandler([&](const QByteArray &request, InstanceService::Reply reply) {
        const auto object = QJsonDocument::fromJson(request).object();
        if (object.value(QStringLiteral("command")).toString() != QStringLiteral("update")) return false;
        updateService.requestCLIUpdate(object, [reply = std::move(reply)](const QJsonObject &response) {
            reply(QJsonDocument(response).toJson(QJsonDocument::Compact));
        });
        return true;
    });
    updateService.setOwnedProcessProvider([&controller] {
        return qMakePair(controller.ownedServerProcessId(), controller.ownedServerExecutablePath());
    });
    QStringList relaunchArguments;
    if (parser.isSet("background")) relaunchArguments << QStringLiteral("--background");
    if (isolated) relaunchArguments << QStringLiteral("--config") << parser.value("config");
    updateService.setRelaunchArguments(relaunchArguments);
    QObject::connect(&updateService, &UpdateService::applyPrepared, &app, [&] {
        controller.stopOwnedServer();
        QTimer::singleShot(0, &app, &QCoreApplication::quit);
    });
    const auto syncServices = [&] {
        startup.setAllowChanges(!capture && !isolated);
        updateService.setPublicTrafficAllowed(!capture && !isolated);
        appInfo.setBackend(capture ? QString() : controller.backendUrl(),
                           capture ? QString() : controller.backendToken(),
                           capture ? QSslCertificate() : controller.backendCertificate(),
                           controller.settings().value(QStringLiteral("mode")).toString() != QStringLiteral("local"));
    };
    QObject::connect(&controller, &Controller::settingsChanged, &app, syncServices);
    QObject::connect(&controller, &Controller::changed, &app, syncServices);
    startup.setPreferenceWriter([&](bool enabled) { return controller.saveStartupPreference(enabled); });
    syncServices();
    QQmlApplicationEngine engine;
    engine.rootContext()->setContextProperty("backend", &controller);
    engine.rootContext()->setContextProperty("startupService", &startup);
    engine.rootContext()->setContextProperty("appInfo", &appInfo);
    engine.rootContext()->setContextProperty("updateService", &updateService);
    const bool hasTray = !capture && QSystemTrayIcon::isSystemTrayAvailable();
    engine.rootContext()->setContextProperty("trayAvailable", hasTray);
    engine.rootContext()->setContextProperty("startHidden", true);
    engine.rootContext()->setContextProperty("captureMode", capture);
    engine.loadFromModule("Headroom", "Main");
    if (engine.rootObjects().isEmpty()) return 1;
    if (parser.isSet("headroom-ready-file")) {
        const QString readyPath = parser.value("headroom-ready-file");
        const QByteArray nonce = qgetenv("HEADROOM_READY_NONCE");
        QSaveFile ready(readyPath);
        const QJsonObject value{{QStringLiteral("nonce"), QString::fromUtf8(nonce)},
            {QStringLiteral("pid"), qint64(QCoreApplication::applicationPid())},
            {QStringLiteral("version"), QCoreApplication::applicationVersion()},
            {QStringLiteral("executable"), QFileInfo(QCoreApplication::applicationFilePath()).canonicalFilePath()}};
        if (nonce.isEmpty() || !QDir::isAbsolutePath(readyPath) || !ready.open(QIODevice::WriteOnly)
            || ready.write(QJsonDocument(value).toJson(QJsonDocument::Compact) + '\n') < 0 || !ready.commit()) return 1;
#ifndef Q_OS_WIN
        QFile::setPermissions(readyPath, QFileDevice::ReadOwner | QFileDevice::WriteOwner);
#endif
    }
    auto window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
    TrayPopup popup(window, hasTray, controller.windowSize(),
        capture ? TrayPopup::SizeWriter{} : TrayPopup::SizeWriter{[&controller](QSize size) {
            controller.saveWindowSize(size);
        }}, &app);
    QSystemTrayIcon tray(TrayVisual::icon({}));
    QMenu fallbackMenu;
    QMenu *trayMenu = &fallbackMenu;
    bool nativeTrayUsed = false;
    TrayAttention attention([&] {
        if (!hasTray || (window->isVisible() && app.applicationState() == Qt::ApplicationActive)
            || trayMenu->isVisible()) return true;
        // SNI/Wayland hosts do not expose icon hover or global pointer position.
        // Never guess from stale Wayland coordinates. Windows and X11 can use
        // the actual tray rectangle; Qt also sends tooltip events on X11.
        const QString platform = QGuiApplication::platformName();
        if (nativeTrayUsed || (platform != "windows" && platform != "xcb" && platform != "cocoa")) return false;
        const QRect bounds = tray.geometry();
        return bounds.isValid() && bounds.contains(QCursor::pos());
    });
    tray.installEventFilter(&attention);
    const auto show = [&] { attention.acknowledge(); popup.show(); };
    QObject::connect(&instance, &InstanceService::activationRequested, &app, show);
    if (!capture && !isolated && controller.startupMigrationPending() &&
        startup.migrateLegacyRegistration(controller.startupPreference()))
        controller.completeStartupMigration();
#ifdef HEADROOM_KDE_TRAY
    std::unique_ptr<KStatusNotifierItem> nativeTray;
    if (hasTray) {
        nativeTray = std::make_unique<KStatusNotifierItem>("headroom");
        nativeTrayUsed = true;
        nativeTray->setTitle("Headroom");
        nativeTray->setCategory(KStatusNotifierItem::ApplicationStatus);
        nativeTray->setStandardActionsEnabled(false);
        nativeTray->setStatus(KStatusNotifierItem::Active);
        trayMenu = new QMenu;
        nativeTray->setContextMenu(trayMenu);
        QObject::connect(nativeTray.get(), &KStatusNotifierItem::activateRequested, &app,
            [&](bool, const QPoint &pos) { attention.acknowledge(); popup.toggle(pos, !pos.isNull()); });
        QObject::connect(nativeTray.get(), &KStatusNotifierItem::secondaryActivateRequested, &attention,
            [&] { attention.acknowledge(); });
        QObject::connect(nativeTray.get(), &KStatusNotifierItem::scrollRequested, &attention,
            [&] { attention.acknowledge(); });
    }
#endif
    QMenu &menu = *trayMenu;
    QObject::connect(&menu, &QMenu::aboutToShow, &attention, &TrayAttention::acknowledge);
    QObject::connect(&menu, &QMenu::triggered, &attention, [&] { attention.acknowledge(); });
    QObject::connect(window, &QWindow::visibleChanged, &attention, [&](bool visible) {
        if (visible) attention.acknowledge();
    });
    QObject::connect(&app, &QGuiApplication::applicationStateChanged, &attention, [&](Qt::ApplicationState state) {
        if (state == Qt::ApplicationActive && window->isVisible()) attention.acknowledge();
    });
    menu.addAction("Open Headroom", &app, show); menu.addAction("Refresh usage", &controller, &Controller::refresh);
    menu.addAction("Settings…", &app, [&] {
        show();
        if (auto settings = window->findChild<QObject *>("settingsPanel")) QMetaObject::invokeMethod(settings, "open");
    });
    menu.addSeparator(); menu.addAction("Quit Headroom", &app, &QApplication::quit);
#ifndef Q_OS_MACOS
    tray.setContextMenu(&fallbackMenu);
#endif
    QObject::connect(&tray, &QSystemTrayIcon::activated, &app, [&](QSystemTrayIcon::ActivationReason reason) {
        if (reason != QSystemTrayIcon::Unknown) attention.acknowledge();
#ifdef Q_OS_MACOS
        if (reason == QSystemTrayIcon::Context) {
            const QPoint anchor = tray.geometry().isValid() ? tray.geometry().center() : QCursor::pos();
            fallbackMenu.popup(anchor);
            return;
        }
#endif
        if (reason == QSystemTrayIcon::Trigger || reason == QSystemTrayIcon::DoubleClick)
            popup.toggle(tray.geometry().isValid() ? tray.geometry().center() : QCursor::pos());
    });
    QObject::connect(&tray, &QSystemTrayIcon::messageClicked, &attention, &TrayAttention::acknowledge);
    const auto notify = [&](const QString &title, const QString &message, int severity) {
        if (!hasTray) return;
#ifdef HEADROOM_KDE_TRAY
        if (nativeTray) {
            nativeTray->showMessage(title, message, severity >= 3 ? "dialog-error" : severity >= 2 ? "dialog-warning" : "dialog-information", 7000);
            return;
        }
#endif
        tray.showMessage(title, message, severity >= 3 ? QSystemTrayIcon::Critical : severity >= 2 ? QSystemTrayIcon::Warning : QSystemTrayIcon::Information, 7000);
    };
    QObject::connect(&controller, &Controller::notify, &app, [&](const QString &title, const QString &message) { notify(title, message, 0); });
    QObject::connect(&controller, &Controller::usageAlert, &app, notify);
    TrayVisual::Model trayModel;
    const auto renderTray = [&] {
        const auto icon = TrayVisual::icon(trayModel, attention.frame());
#ifdef HEADROOM_KDE_TRAY
        if (nativeTray) {
            nativeTray->setIconByPixmap(icon);
            return;
        }
#endif
        tray.setIcon(icon);
    };
    QObject::connect(&attention, &TrayAttention::frameChanged, &app, renderTray);
    auto updateTray = [&] {
        trayModel = TrayVisual::build(controller.state(), controller.providers(), controller.primary(),
            [&](const QString &provider, const QVariantMap &bucket) { return controller.concern(provider, bucket); });
        {
            // The model and attention frame are one visual update. Timer-driven
            // frames still render independently between provider polls.
            const QSignalBlocker block(&attention);
            attention.update(trayModel);
        }
        renderTray();
#ifdef HEADROOM_KDE_TRAY
        if (nativeTray) {
            nativeTray->setToolTipTitle(trayModel.tooltip.section('\n', 0, 0));
            nativeTray->setToolTipSubTitle(trayModel.tooltip.section('\n', 1));
            return;
        }
#endif
        tray.setToolTip(trayModel.tooltip);
    };
    QObject::connect(&controller, &Controller::changed, &app, updateTray);
    QObject::connect(&controller, &Controller::settingsChanged, &app, updateTray);
    updateTray();
    if (hasTray) {
        app.setQuitOnLastWindowClosed(false);
#ifdef HEADROOM_KDE_TRAY
        if (!nativeTray) tray.show();
#else
        tray.show();
#endif
    }
    if (!parser.isSet("background") || !hasTray || parser.isSet("headroom-update-restart")
        || parser.isSet("headroom-installed-restart")) show();
    if (!capture && !isolated) QTimer::singleShot(2500, &updateService, &UpdateService::startAutomaticCheck);
    if (capture) QTimer::singleShot(900, &app, [&] { app.exit(window->grabWindow().save(parser.value("screenshot")) ? 0 : 2); });
    return app.exec();
}
