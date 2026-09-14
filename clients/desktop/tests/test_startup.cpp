#include "startup.h"

#include <QDir>
#include <QCoreApplication>
#include <QFile>
#include <QProcess>
#include <QProcessEnvironment>
#include <QSignalSpy>
#include <QStandardPaths>
#include <QSettings>
#include <QTemporaryDir>
#include <QUuid>
#include <QJsonDocument>
#include <QJsonObject>
#include <QtTest>

namespace {
bool writeFile(const QString &path, const QByteArray &contents, bool executable = false)
{
    if (!QDir().mkpath(QFileInfo(path).absolutePath())) return false;
    QFile file(path);
    if (!file.open(QIODevice::WriteOnly) || file.write(contents) != contents.size()) return false;
    file.close();
    return !executable || file.setPermissions(QFile::ReadOwner | QFile::WriteOwner | QFile::ExeOwner);
}
QByteArray readFile(const QString &path)
{
    QFile file(path);
    return file.open(QIODevice::ReadOnly) ? file.readAll() : QByteArray{};
}
// Frozen v2.0.1 format: migration must recognize the entry installed before
// bundle attribution was added, while preserving the exact ownership check.
QByteArray legacyMacLaunchAgent(const QString &executable)
{
    return QStringLiteral(
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
        "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n"
        "<plist version=\"1.0\">\n<dict>\n"
        "  <key>Label</key>\n  <string>io.headroom.Headroom</string>\n"
        "  <key>ProgramArguments</key>\n  <array>\n    <string>%1</string>\n    <string>--background</string>\n  </array>\n"
        "  <key>RunAtLoad</key>\n  <true/>\n"
        "</dict>\n</plist>\n").arg(executable.toHtmlEscaped()).toUtf8();
}
QString currentExecutable()
{
    return QCoreApplication::applicationFilePath();
}
}

class StartupTest : public QObject {
    Q_OBJECT
private slots:
    void packagedLauncherPathIsUsedOnlyForMatchingInstall()
    {
        QTemporaryDir dir;
        QVERIFY(dir.isValid());
        const QString root = QFileInfo(dir.path()).canonicalFilePath();
        QVERIFY(!root.isEmpty());
        const QString launcher = QDir(root).filePath("external/headroom");
        QVERIFY(writeFile(launcher, "fixture", true));
        QVERIFY(writeFile(launcher + QStringLiteral(".root"), QDir::toNativeSeparators(root).toUtf8() + '\n'));
        qputenv("HEADROOM_INSTALL_ROOT", root.toUtf8());
        qputenv("HEADROOM_LAUNCHER_PATH", launcher.toUtf8());
        qputenv("HEADROOM_PACKAGE_VERSION", "9.8.7");
        QCOMPARE(StartupService::defaultExecutablePath(), QCoreApplication::applicationFilePath());
#ifdef Q_OS_WIN
        const QString relativeApplication = QStringLiteral("bin/headroom.exe");
#elif defined(Q_OS_MACOS)
        const QString relativeApplication = QStringLiteral("Headroom.app/Contents/MacOS/headroom");
#else
        const QString relativeApplication = QStringLiteral("bin/headroom");
#endif
        const QString packaged = QDir(root).filePath(QStringLiteral("versions/9.8.7/") + relativeApplication);
        QVERIFY(writeFile(packaged, "fixture", true));
        QCOMPARE(StartupService::packagedExecutablePath(packaged), QFileInfo(launcher).absoluteFilePath());
        const QString generation = QDir(root).filePath(QStringLiteral("versions/9.8.7.generation-0123456789abcdef-fedcba9876543210/") + relativeApplication);
        QVERIFY(writeFile(generation, "fixture", true));
        QCOMPARE(StartupService::packagedExecutablePath(generation), QFileInfo(launcher).absoluteFilePath());
        const QString macApplication = QDir(root).filePath(
            QStringLiteral("versions/9.8.7/Headroom.app/Contents/MacOS/headroom"));
        QVERIFY(writeFile(macApplication, "fixture", true));
        QCOMPARE(StartupService::packagedExecutablePath(macApplication, StartupService::Platform::Mac),
                 QFileInfo(launcher).absoluteFilePath());
#ifdef Q_OS_WIN
        QString generationCaseAlias = generation;
        generationCaseAlias.replace(QStringLiteral("generation-0123456789abcdef"),
                                    QStringLiteral("GENERATION-0123456789ABCDEF"));
        QCOMPARE(StartupService::packagedExecutablePath(generationCaseAlias), QFileInfo(launcher).absoluteFilePath());
#endif

        for (const QString &unsafe : {
                 QDir(root).filePath(QStringLiteral("versions/9.8.6.generation-0123456789abcdef-fedcba9876543210/") + relativeApplication),
                 QDir(root).filePath(QStringLiteral("versions/9.8.7.generation-1123456789ABCDEf-fedcba9876543210/") + relativeApplication),
                 QDir(root).filePath(QStringLiteral("versions/9.8.7.generation-0123456789abcdef-short/") + relativeApplication),
                 QDir(root).filePath(QStringLiteral("foreign/") + relativeApplication)}) {
            QVERIFY(writeFile(unsafe, "fixture", true));
            QCOMPARE(StartupService::packagedExecutablePath(unsafe), QFileInfo(unsafe).absoluteFilePath());
        }
        QVERIFY(writeFile(launcher + QStringLiteral(".root"), QByteArrayLiteral("/wrong/root\n")));
        QCOMPARE(StartupService::packagedExecutablePath(generation), QFileInfo(generation).absoluteFilePath());
        qunsetenv("HEADROOM_INSTALL_ROOT"); qunsetenv("HEADROOM_LAUNCHER_PATH"); qunsetenv("HEADROOM_PACKAGE_VERSION");
    }

    void repairsPriorGenerationStartupEntryToStableLauncher()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString rootAlias = dir.filePath(QStringLiteral("install"));
        QVERIFY(QDir().mkpath(rootAlias));
        const QString root = QFileInfo(rootAlias).canonicalFilePath();
        QVERIFY(!root.isEmpty());
        const QString launcher = dir.filePath(QStringLiteral("bin/headroom"));
#ifdef Q_OS_WIN
        const QString appName = QStringLiteral("headroom.exe");
#elif defined(Q_OS_MACOS)
        const QString appName = QStringLiteral("Headroom.app/Contents/MacOS/headroom");
#else
        const QString appName = QStringLiteral("headroom");
#endif
        const QString applicationPrefix =
#ifdef Q_OS_MACOS
            QString();
#else
            QStringLiteral("bin/");
#endif
        const QString current = QDir(root).filePath(QStringLiteral("versions/2.0.0.generation-0123456789abcdef-fedcba9876543210/") + applicationPrefix + appName);
        const QString previous = QDir(root).filePath(QStringLiteral("versions/1.8.0.generation-1111111111111111-2222222222222222/") + applicationPrefix + appName);
        QVERIFY(writeFile(current, "current", true)); QVERIFY(writeFile(previous, "previous", true));
        QVERIFY(writeFile(launcher, "launcher", true));
        QVERIFY(writeFile(launcher + QStringLiteral(".root"), QDir::toNativeSeparators(root).toUtf8() + '\n'));
        qputenv("HEADROOM_INSTALL_ROOT", root.toUtf8()); qputenv("HEADROOM_LAUNCHER_PATH", launcher.toUtf8());
        qputenv("HEADROOM_PACKAGE_VERSION", "2.0.0");

#ifdef Q_OS_WIN
        const QString registryPath = QStringLiteral("HKEY_CURRENT_USER\\Software\\HeadroomTests\\")
            + QUuid::createUuid().toString(QUuid::WithoutBraces);
        QSettings registry(registryPath, QSettings::NativeFormat);
        const QString oldCommand = QStringLiteral("\"") + QDir::toNativeSeparators(previous) + QStringLiteral("\" --background");
        const QString stableCommand = QStringLiteral("\"") + QDir::toNativeSeparators(launcher) + QStringLiteral("\" --background");
        registry.setValue(QStringLiteral("Headroom"), oldCommand); registry.sync();
        StartupService repaired({}, current, true, nullptr, StartupService::Platform::Windows, registryPath);
        QCOMPARE(registry.value(QStringLiteral("Headroom")).toString(), stableCommand);
        registry.setValue(QStringLiteral("Headroom"), oldCommand + QStringLiteral(" --custom")); registry.sync();
        StartupService custom({}, current, true, nullptr, StartupService::Platform::Windows, registryPath);
        QCOMPARE(registry.value(QStringLiteral("Headroom")).toString(), oldCommand + QStringLiteral(" --custom"));
        registry.setValue(QStringLiteral("Headroom"), oldCommand); registry.sync();
        StartupService preview({}, current, false, nullptr, StartupService::Platform::Windows, registryPath);
        QCOMPARE(registry.value(QStringLiteral("Headroom")).toString(), oldCommand);
        registry.remove(QStringLiteral("Headroom"));
        registry.setValue(QStringLiteral("ClaudeUsageWidget"), QStringLiteral("legacy")); registry.sync();
        StartupService migration({}, current, true, nullptr, StartupService::Platform::Windows, registryPath);
        migration.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(migration.migrateLegacyRegistration(true));
        QCOMPARE(registry.value(QStringLiteral("Headroom")).toString(), stableCommand);
        QVERIFY(!registry.contains(QStringLiteral("ClaudeUsageWidget")));
        registry.clear(); registry.sync();
#elif defined(Q_OS_MACOS)
        const QString config = dir.filePath(QStringLiteral("LaunchAgents"));
        StartupService seed(config, previous, true, nullptr, StartupService::Platform::Mac);
        seed.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(seed.setEnabled(true));
        const QByteArray oldEntry = readFile(seed.entryPath());
        StartupService repaired(config, current, true, nullptr, StartupService::Platform::Mac);
        QVERIFY(repaired.enabled());
        const QByteArray repairedEntry = readFile(repaired.entryPath());
        QVERIFY(repairedEntry.contains(launcher.toHtmlEscaped().toUtf8()));
        QVERIFY(!repairedEntry.contains(previous.toUtf8()));

        QVERIFY(writeFile(repaired.entryPath(), QByteArray(oldEntry).replace("--background", "--background-custom")));
        QFile::setPermissions(repaired.entryPath(), QFile::ReadOwner | QFile::WriteOwner);
        StartupService custom(config, current, true, nullptr, StartupService::Platform::Mac);
        QCOMPARE(readFile(custom.entryPath()), QByteArray(oldEntry).replace("--background", "--background-custom"));

        QVERIFY(writeFile(custom.entryPath(), oldEntry));
        QFile::setPermissions(custom.entryPath(), QFile::ReadOwner | QFile::WriteOwner);
        StartupService preview(config, current, false, nullptr, StartupService::Platform::Mac);
        QCOMPARE(readFile(preview.entryPath()), oldEntry);
#else
        const QString config = dir.filePath(QStringLiteral("config"));
        const QString entry = QDir(config).filePath(QStringLiteral("autostart/headroom.desktop"));
        const QByteArray oldEntry = QStringLiteral("[Desktop Entry]\nType=Application\nExec=\"%1\" --background\nX-GNOME-Autostart-enabled=true\n")
            .arg(previous).toUtf8();
        QVERIFY(writeFile(entry, oldEntry));
        StartupService repaired(config, current, true);
        QVERIFY(repaired.enabled());
        const QByteArray repairedEntry = readFile(entry);
        QVERIFY(repairedEntry.contains((QStringLiteral("Exec=\"") + launcher + QStringLiteral("\" --background")).toUtf8()));
        QVERIFY(!repairedEntry.contains(previous.toUtf8()));

        const QString publicCLI = dir.filePath(QStringLiteral("public bin/headroom"));
        QVERIFY(writeFile(publicCLI, "CLI router", true));
        QVERIFY(writeFile(publicCLI + QStringLiteral(".root"), QDir::toNativeSeparators(root).toUtf8() + '\n'));
        QVERIFY(writeFile(QDir(root).filePath(QStringLiteral("install-state.json")),
            QJsonDocument(QJsonObject{{"schema", 1}, {"product", "Headroom"}, {"cli_entry_path", publicCLI}}).toJson()));
        const QByteArray oldPublicEntry = QByteArray(oldEntry).replace(previous.toUtf8(), publicCLI.toUtf8());
        QVERIFY(writeFile(entry, oldPublicEntry));
        StartupService migratedCLI(config, current, true);
        QVERIFY(migratedCLI.enabled());
        QVERIFY(readFile(entry).contains(launcher.toUtf8()));
        QVERIFY(!readFile(entry).contains(publicCLI.toUtf8()));
        QVERIFY(writeFile(entry, oldPublicEntry));
        QVERIFY(writeFile(publicCLI + QStringLiteral(".root"), "/different-install\n"));
        StartupService unrelatedCLI(config, current, true);
        QCOMPARE(readFile(entry), oldPublicEntry);

        QVERIFY(writeFile(entry, QByteArray(oldEntry).replace("X-GNOME-Autostart-enabled=true", "X-GNOME-Autostart-enabled=false")));
        StartupService disabled(config, current, true);
        QVERIFY(!disabled.enabled()); QCOMPARE(readFile(entry), QByteArray(oldEntry).replace("X-GNOME-Autostart-enabled=true", "X-GNOME-Autostart-enabled=false"));

        const QByteArray hiddenEntry = oldEntry + QByteArrayLiteral("Hidden = true\n");
        QVERIFY(writeFile(entry, hiddenEntry));
        StartupService hidden(config, current, true);
        QVERIFY(!hidden.enabled()); QCOMPARE(readFile(entry), hiddenEntry);

        const QByteArray duplicateEntry = oldEntry + QByteArrayLiteral("Type=Application\n");
        QVERIFY(writeFile(entry, duplicateEntry));
        StartupService duplicate(config, current, true);
        QCOMPARE(readFile(entry), duplicateEntry);

        const QByteArray custom = QStringLiteral("[Desktop Entry]\nType=Application\nExec=\"%1\" --background --custom\n").arg(previous).toUtf8();
        QVERIFY(writeFile(entry, custom));
        StartupService unrelated(config, current, true);
        QCOMPARE(readFile(entry), custom);

        QVERIFY(writeFile(entry, oldEntry));
        StartupService preview(config, current, false);
        QCOMPARE(readFile(entry), oldEntry);
#endif
        qunsetenv("HEADROOM_INSTALL_ROOT"); qunsetenv("HEADROOM_LAUNCHER_PATH"); qunsetenv("HEADROOM_PACKAGE_VERSION");
    }
    void preferenceWriterTracksBothToggles()
    {
        QTemporaryDir dir;
        QList<bool> writes;
        StartupService service(dir.filePath("config"), currentExecutable(), true, nullptr,
            StartupService::Platform::Linux);
        service.setPreferenceWriter([&](bool enabled) { writes.append(enabled); return QString(); });
        QVERIFY(service.setEnabled(true));
        QVERIFY(service.setEnabled(false));
        QCOMPARE(writes, QList<bool>({true, false}));
        QVERIFY(!QFileInfo::exists(service.entryPath()));

        service.setPreferenceWriter([](bool) { return QString("Settings could not be saved."); });
        QVERIFY(!service.setEnabled(true));
        QVERIFY(!QFileInfo::exists(service.entryPath()));
        QVERIFY(!service.error().isEmpty());
    }

    void macLaunchAgentIsRemovableAfterTheExecutableDisappears()
    {
        // An external reinstall can delete the generation an owned entry points
        // at. Start at login must still be switchable off instead of leaving a
        // stale plist that launches a missing binary at every login.
        QTemporaryDir dir; QVERIFY(dir.isValid());
#ifdef Q_OS_WIN
        const QString executable = dir.filePath(QStringLiteral("versions/1.0.0/Headroom.app/Contents/MacOS/headroom.exe"));
        QVERIFY(QDir().mkpath(QFileInfo(executable).absolutePath()));
        QVERIFY(QFile::copy(currentExecutable(), executable));
#else
        const QString executable = dir.filePath(QStringLiteral("versions/1.0.0/Headroom.app/Contents/MacOS/headroom"));
        QVERIFY(writeFile(executable, QByteArrayLiteral("fixture"), true));
#endif
        StartupService service(dir.filePath(QStringLiteral("LaunchAgents")), executable, true, nullptr,
                               StartupService::Platform::Mac);
        bool stored = false;
        service.setPreferenceWriter([&](bool enabled) { stored = enabled; return QString(); });
        QVERIFY2(service.setEnabled(true), qPrintable(service.error()));
        QVERIFY(QFileInfo::exists(service.entryPath()));

        QVERIFY(QFile::remove(executable));
        QVERIFY(service.setEnabled(false));
        QVERIFY(!stored);
        QVERIFY(!QFileInfo::exists(service.entryPath()));
        QVERIFY(service.error().isEmpty());

        // Enabling still requires a usable binary, and an unrecognized entry is
        // never removed just because the executable is gone.
        QVERIFY(!service.setEnabled(true));
        QVERIFY(!QFileInfo::exists(service.entryPath()));
        QVERIFY(writeFile(service.entryPath(), QByteArrayLiteral("<plist>foreign</plist>"), true));
        QVERIFY(!service.setEnabled(false));
        QCOMPARE(readFile(service.entryPath()), QByteArrayLiteral("<plist>foreign</plist>"));
    }

    void macLaunchAgentIsPrivateExactAndTransactional()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
#ifdef Q_OS_WIN
        const QString executable = dir.filePath(QStringLiteral("Head room & test/headroom.exe"));
        QVERIFY(QDir().mkpath(QFileInfo(executable).absolutePath()));
        QVERIFY(QFile::copy(currentExecutable(), executable));
#else
        const QString executable = dir.filePath(QStringLiteral("Head room & <test>/headroom"));
        QVERIFY(writeFile(executable, QByteArrayLiteral("fixture"), true));
#endif
        StartupService service(dir.filePath(QStringLiteral("LaunchAgents")), executable, true, nullptr,
                               StartupService::Platform::Mac);
        QCOMPARE(service.entryPath(), dir.filePath(QStringLiteral("LaunchAgents/io.headroom.Headroom.plist")));
        service.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(service.setEnabled(true));
        QVERIFY(service.enabled());
        const QByteArray original = readFile(service.entryPath());
        QVERIFY(original.contains("<string>io.headroom.Headroom</string>"));
        QVERIFY(original.contains("<string>--background</string>"));
#ifdef Q_OS_WIN
        QVERIFY(original.contains("Head room &amp; test/headroom.exe"));
#else
        QVERIFY(original.contains("Head room &amp; &lt;test&gt;/headroom"));
        QVERIFY(!(QFile::permissions(service.entryPath()) &
            (QFileDevice::ReadGroup | QFileDevice::WriteGroup | QFileDevice::ReadOther | QFileDevice::WriteOther)));
#endif

        StartupService reloaded(dir.filePath(QStringLiteral("LaunchAgents")), executable, true, nullptr,
                                StartupService::Platform::Mac);
        QVERIFY(reloaded.enabled());
        reloaded.setPreferenceWriter([](bool) { return QStringLiteral("Settings could not be saved."); });
        QVERIFY(!reloaded.setEnabled(false));
        QCOMPARE(readFile(reloaded.entryPath()), original);
        QVERIFY(reloaded.enabled());
        reloaded.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(reloaded.setEnabled(false));
        QVERIFY(!QFileInfo::exists(reloaded.entryPath()));
    }

    void macPriorGenerationEntryRepairsToStableLauncher_data()
    {
        QTest::addColumn<bool>("stableEntry");
        QTest::addColumn<bool>("legacyFormat");
        QTest::newRow("prior-generation") << false << false;
        QTest::newRow("legacy-prior-generation") << false << true;
        QTest::newRow("stable-launcher") << true << false;
        QTest::newRow("legacy-stable-launcher") << true << true;
    }

    void macPriorGenerationEntryRepairsToStableLauncher()
    {
        QFETCH(bool, stableEntry);
        QFETCH(bool, legacyFormat);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString root = QFileInfo(dir.path()).canonicalFilePath();
        const QString launcher = QDir(root).filePath(QStringLiteral("stable/Headroom.app/Contents/MacOS/headroom"));
        const QString current = QDir(root).filePath(QStringLiteral(
            "versions/2.0.0.generation-0123456789abcdef-fedcba9876543210/Headroom.app/Contents/MacOS/headroom"));
        const QString previous = QDir(root).filePath(QStringLiteral(
            "versions/1.8.0.generation-1111111111111111-2222222222222222/Headroom.app/Contents/MacOS/headroom"));
        QVERIFY(writeFile(current, "current"));
        QVERIFY(writeFile(previous, "previous"));
        QVERIFY(writeFile(launcher, "launcher"));
        QVERIFY(writeFile(launcher + QStringLiteral(".root"), QDir::toNativeSeparators(root).toUtf8() + '\n'));
        const QString launchAgents = QDir(root).filePath(QStringLiteral("LaunchAgents"));
        StartupService seed(launchAgents, currentExecutable(), true, nullptr, StartupService::Platform::Mac);
        seed.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(seed.setEnabled(true));
        QByteArray oldEntry = readFile(seed.entryPath());
        const QString oldExecutable = stableEntry ? launcher : previous;
        if (legacyFormat) oldEntry = legacyMacLaunchAgent(oldExecutable);
        else oldEntry.replace(currentExecutable().toHtmlEscaped().toUtf8(), oldExecutable.toHtmlEscaped().toUtf8());
        QVERIFY(writeFile(seed.entryPath(), oldEntry));
        QVERIFY(QFile::setPermissions(seed.entryPath(), QFile::ReadOwner | QFile::WriteOwner));

        qputenv("HEADROOM_INSTALL_ROOT", root.toUtf8());
        qputenv("HEADROOM_LAUNCHER_PATH", launcher.toUtf8());
        qputenv("HEADROOM_PACKAGE_VERSION", "2.0.0");
        StartupService repaired(launchAgents, current, true, nullptr, StartupService::Platform::Mac);
        qunsetenv("HEADROOM_INSTALL_ROOT");
        qunsetenv("HEADROOM_LAUNCHER_PATH");
        qunsetenv("HEADROOM_PACKAGE_VERSION");
        QVERIFY(repaired.enabled());
        const QByteArray entry = readFile(repaired.entryPath());
        QVERIFY(entry.contains(launcher.toHtmlEscaped().toUtf8()));
        QVERIFY(!entry.contains(previous.toUtf8()));
        QVERIFY(entry.contains("<key>AssociatedBundleIdentifiers</key>"));
        QVERIFY(entry.contains("<string>io.headroom.launcher</string>"));
    }

    void macLegacyEntryRemainsEnabledAndRemovable()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        StartupService service(dir.path(), currentExecutable(), true, nullptr, StartupService::Platform::Mac);
        const QByteArray legacy = legacyMacLaunchAgent(currentExecutable());
        QVERIFY(writeFile(service.entryPath(), legacy));
        QVERIFY(QFile::setPermissions(service.entryPath(), QFile::ReadOwner | QFile::WriteOwner));
        service.refresh();
        QVERIFY(service.enabled());
        QVERIFY(service.setEnabled(false));
        QVERIFY(!QFileInfo::exists(service.entryPath()));
        QVERIFY(writeFile(service.entryPath(), legacy));
        QVERIFY(QFile::setPermissions(service.entryPath(), QFile::ReadOwner | QFile::WriteOwner));
        QVERIFY(service.setEnabled(true));
        QVERIFY(readFile(service.entryPath()).contains("<key>AssociatedBundleIdentifiers</key>"));

        QByteArray modified = readFile(service.entryPath());
        modified.replace("io.headroom.launcher", "io.foreign.launcher");
        QVERIFY(writeFile(service.entryPath(), modified));
        service.refresh();
        QVERIFY(!service.enabled());
        QVERIFY(!service.setEnabled(true));
        QVERIFY(!service.setEnabled(false));
        QCOMPARE(readFile(service.entryPath()), modified);
    }

    void macLaunchAgentPreservesForeignEntries()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString executable = dir.filePath(QStringLiteral("headroom"));
        QVERIFY(writeFile(executable, QByteArrayLiteral("fixture"), true));
        StartupService service(dir.filePath(QStringLiteral("LaunchAgents")), executable, true, nullptr,
                               StartupService::Platform::Mac);
        const QByteArray foreign = QByteArrayLiteral("<?xml version=\"1.0\"?><plist><dict><key>Label</key><string>foreign</string></dict></plist>");
        QVERIFY(writeFile(service.entryPath(), foreign));
        QVERIFY(QFile::setPermissions(service.entryPath(), QFile::ReadOwner | QFile::WriteOwner));
        QVERIFY(!service.setEnabled(true));
        QCOMPARE(readFile(service.entryPath()), foreign);
        QVERIFY(!service.setEnabled(false));
        QCOMPARE(readFile(service.entryPath()), foreign);
    }
#ifdef Q_OS_WIN
    void windowsRegistrationAndLegacyMigration()
    {
        const QString registryPath = "HKEY_CURRENT_USER\\Software\\HeadroomTests\\" +
                                     QUuid::createUuid().toString(QUuid::WithoutBraces);
        QSettings registry(registryPath, QSettings::NativeFormat);
        registry.setValue("ClaudeUsageWidget", "\"C:\\Legacy App\\ClaudeUsageWidget.exe\"");
        registry.sync();
        bool preference = false;
        StartupService service({}, currentExecutable(), true, nullptr,
            StartupService::Platform::Windows, registryPath);
        service.setPreferenceWriter([&](bool enabled) { preference = enabled; return QString(); });
        QVERIFY(service.migrateLegacyRegistration(true));
        QCOMPARE(preference, true);
        const QString expected = "\"" + QDir::toNativeSeparators(currentExecutable()) + "\" --background";
        QCOMPARE(registry.value("Headroom").toString(), expected);
        QVERIFY(!registry.contains("ClaudeUsageWidget"));
        QVERIFY(service.enabled());
        QVERIFY(service.setEnabled(false));
        QCOMPARE(preference, false);
        QVERIFY(!registry.contains("Headroom"));
        registry.clear(); registry.sync();
    }

    void windowsPersistenceFailureRollsBackAndRemainsRetryable()
    {
        const QString registryPath = "HKEY_CURRENT_USER\\Software\\HeadroomTests\\" +
                                     QUuid::createUuid().toString(QUuid::WithoutBraces);
        QSettings registry(registryPath, QSettings::NativeFormat);
        registry.setValue("ClaudeUsageWidget", "legacy"); registry.sync();
        StartupService service({}, currentExecutable(), true, nullptr,
            StartupService::Platform::Windows, registryPath);
        service.setPreferenceWriter([](bool) { return QString("Could not persist the startup preference."); });
        QVERIFY(!service.migrateLegacyRegistration(true));
        QVERIFY(!registry.contains("Headroom"));
        QVERIFY(registry.contains("ClaudeUsageWidget"));
        QVERIFY(!service.error().isEmpty());
        service.setPreferenceWriter([](bool) { return QString(); });
        QVERIFY(service.migrateLegacyRegistration(true));
        QVERIFY(registry.contains("Headroom"));
        QVERIFY(!registry.contains("ClaudeUsageWidget"));
        registry.clear(); registry.sync();
    }
#endif
    void optInPersistenceAndRemoval()
    {
#if defined(Q_OS_WIN) || defined(Q_OS_MACOS)
        QSKIP("XDG autostart entries are Linux-specific");
#endif
        QTemporaryDir dir;
        QVERIFY(dir.isValid());
        const QString executable = dir.filePath("bin/headroom");
        QVERIFY(writeFile(executable, "#!/bin/sh\nexit 0\n", true));
        StartupService service(dir.filePath("config"), executable);
        QVERIFY(!service.enabled());
        QVERIFY(!QFileInfo::exists(dir.filePath("config")));
        QSignalSpy changed(&service, &StartupService::enabledChanged);
        QVERIFY(service.setEnabled(true));
        QVERIFY(service.enabled());
        QCOMPARE(changed.size(), 1);
        const QByteArray original = readFile(service.entryPath());
        QVERIFY(original.contains(" --background\n"));
        StartupService reloaded(dir.filePath("config"), executable);
        QVERIFY(reloaded.enabled());
        QVERIFY(service.setEnabled(true));
        QCOMPARE(readFile(service.entryPath()), original);
        QCOMPARE(changed.size(), 1);
        const QString other = dir.filePath("config/autostart/another.desktop");
        QVERIFY(writeFile(other, "keep me"));
        QVERIFY(service.setEnabled(false));
        QVERIFY(!service.enabled());
        QVERIFY(!QFileInfo::exists(service.entryPath()));
        QCOMPARE(readFile(other), QByteArray("keep me"));
        QCOMPARE(changed.size(), 2);
        QVERIFY(service.setEnabled(false));
    }

    void previewNeverMutates()
    {
        QTemporaryDir dir;
#ifdef Q_OS_WIN
        const QString registryPath = "HKEY_CURRENT_USER\\Software\\HeadroomTests\\" +
                                     QUuid::createUuid().toString(QUuid::WithoutBraces);
        QSettings registry(registryPath, QSettings::NativeFormat);
        StartupService service(dir.filePath("config"), currentExecutable(), false, nullptr,
            StartupService::Platform::Windows, registryPath);
#else
        StartupService service(dir.filePath("config"), currentExecutable(), false);
#endif
        QVERIFY(!service.available());
        QVERIFY(!service.setEnabled(true));
        QVERIFY(!service.error().isEmpty());
#ifdef Q_OS_WIN
        QVERIFY(!registry.contains("Headroom"));
#else
        QVERIFY(!QFileInfo::exists(dir.filePath("config")));
#endif
        service.setAllowChanges(true);
#ifdef Q_OS_WIN
        QVERIFY(service.available());
        QVERIFY(service.error().isEmpty());
        QVERIFY(service.setEnabled(true));
        const QString registered = registry.value("Headroom").toString();
        QVERIFY(!registered.isEmpty());
        service.setAllowChanges(false);
        QVERIFY(!service.setEnabled(false));
        QCOMPARE(registry.value("Headroom").toString(), registered);
        QVERIFY(service.enabled());
        registry.clear(); registry.sync();
        return;
#else
        QVERIFY(service.available());
        QVERIFY(service.error().isEmpty());
        QVERIFY(service.setEnabled(true));
        const QByteArray original = readFile(service.entryPath());
        service.setAllowChanges(false);
        QVERIFY(!service.setEnabled(false));
        QCOMPARE(readFile(service.entryPath()), original);
        QVERIFY(service.enabled());
#endif
    }

    void disabledEntryAndWriteFailure()
    {
#if defined(Q_OS_WIN) || defined(Q_OS_MACOS)
        QSKIP("XDG autostart entries are Linux-specific");
#endif
        QTemporaryDir dir;
        StartupService service(dir.path(), currentExecutable());
        QVERIFY(writeFile(service.entryPath(), "[Desktop Entry]\nType=Application\nExec=headroom\nHidden=true\n"));
        service.refresh();
        QVERIFY(!service.enabled());
        QVERIFY(service.setEnabled(true));
        QVERIFY(service.enabled());
        QVERIFY(service.setEnabled(false));
        QVERIFY(QDir().mkdir(service.entryPath()));
        QVERIFY(!service.setEnabled(true));
        QVERIFY(!service.enabled());
        QVERIFY(!service.error().isEmpty());
        QVERIFY(QFileInfo(service.entryPath()).isDir());
    }

    void invalidExecutablePreservesExistingEntry()
    {
#if defined(Q_OS_WIN) || defined(Q_OS_MACOS)
        QSKIP("Desktop Entry executable validation is Linux-specific");
#endif
        QTemporaryDir dir;
        for (const QString path : {QString("relative/headroom"), dir.filePath("missing/headroom"),
                                   dir.filePath("a=b"), dir.filePath("percent%F"), dir.filePath("headroom\nHidden=true")}) {
            StartupService service(dir.path(), path);
            const QByteArray entry = "[Desktop Entry]\nType=Application\nExec=headroom\n";
            QVERIFY(writeFile(service.entryPath(), entry));
            QVERIFY(!service.setEnabled(true));
            QCOMPARE(readFile(service.entryPath()), entry);
            QVERIFY(service.enabled());
        }
    }

    void respectsXdgConfigHome()
    {
#if defined(Q_OS_WIN) || defined(Q_OS_MACOS)
        QSKIP("XDG_CONFIG_HOME is Linux-specific");
#endif
        QTemporaryDir dir;
        const bool existed = qEnvironmentVariableIsSet("XDG_CONFIG_HOME");
        const QByteArray previous = qgetenv("XDG_CONFIG_HOME");
        qputenv("XDG_CONFIG_HOME", dir.path().toUtf8());
        StartupService service({}, currentExecutable());
        qputenv("XDG_CONFIG_HOME", "relative/ignored");
        StartupService relative({}, currentExecutable());
        if (existed) qputenv("XDG_CONFIG_HOME", previous); else qunsetenv("XDG_CONFIG_HOME");
        QCOMPARE(service.entryPath(), dir.filePath("autostart/headroom.desktop"));
        QCOMPARE(relative.entryPath(), QDir::homePath() + "/.config/autostart/headroom.desktop");
        QVERIFY(!QFileInfo::exists(service.entryPath()));
    }

    void desktopLauncherPreservesSpecialCharacters()
    {
#if defined(Q_OS_WIN) || defined(Q_OS_MACOS)
        QSKIP("GIO desktop entry launching is Linux-specific");
#endif
        const QString gio = QStandardPaths::findExecutable("gio");
        if (gio.isEmpty()) QSKIP("GIO is unavailable for desktop entry launch verification");
        QTemporaryDir dir;
        const QString executable = dir.filePath("Head room '$`\\;special\"/headroom");
        QVERIFY(writeFile(executable, "#!/bin/sh\nprintf '%s\\n' \"$0\" \"$@\" > \"$HEADROOM_STARTUP_TEST_CAPTURE\"\n", true));
        StartupService service(dir.filePath("config"), executable);
        QVERIFY(service.setEnabled(true));
        const QString validator = QStandardPaths::findExecutable("desktop-file-validate");
        if (!validator.isEmpty()) QCOMPARE(QProcess::execute(validator, {service.entryPath()}), 0);
        QProcess launch;
        auto environment = QProcessEnvironment::systemEnvironment();
        const QString capture = dir.filePath("arguments.txt");
        environment.insert("HEADROOM_STARTUP_TEST_CAPTURE", capture);
        launch.setProcessEnvironment(environment);
        launch.start(gio, {"launch", service.entryPath()});
        QVERIFY(launch.waitForFinished());
        QVERIFY2(launch.exitCode() == 0, launch.readAllStandardError().constData());
        QTRY_COMPARE(readFile(capture), executable.toUtf8() + "\n--background\n");
    }
};

QTEST_GUILESS_MAIN(StartupTest)
#include "test_startup.moc"
