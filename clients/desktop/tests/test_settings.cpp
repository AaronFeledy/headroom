#include "settings.h"
#include "tls_fixture.h"

#include <QDir>
#include <QFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTemporaryDir>
#include <QStandardPaths>
#include <QtTest>

namespace {
bool writeFile(const QString &path, const QByteArray &data)
{
    QDir().mkpath(QFileInfo(path).absolutePath());
    QFile file(path);
    return file.open(QIODevice::WriteOnly) && file.write(data) == data.size();
}
QByteArray readFile(const QString &path)
{
    QFile file(path);
    return file.open(QIODevice::ReadOnly) ? file.readAll() : QByteArray{};
}
QJsonObject readObject(const QString &path)
{
    return QJsonDocument::fromJson(readFile(path)).object();
}
}

class SettingsTest : public QObject {
    Q_OBJECT
    QByteArray previousXdg;
    QByteArray previousAppData;
    QString previousOrganization;
    QString previousApplication;
private slots:
    void certificateAndBrowserSharingPersistence() {
        QTemporaryDir dir; SettingsService service(dir.filePath("settings.json"), false);
        QVERIFY(service.value().shareBrowserSignIns); QVERIFY(service.value().remoteCertificate.isEmpty());
        auto settings = service.value(); settings.connectionMode = "remote"; settings.url = "https://example.test:7823";
        QVERIFY(service.save(settings).isEmpty());
        settings.remoteCertificate = QString::fromUtf8(TlsFixture::certificate().toPem()); settings.shareBrowserSignIns = false;
        QVERIFY(service.save(settings).isEmpty());
        SettingsService reopened(service.path(), false);
        QCOMPARE(reopened.value().remoteCertificate, settings.remoteCertificate); QVERIFY(!reopened.value().shareBrowserSignIns);
        settings.url = "https://example.test:7823/prefix"; QVERIFY(service.save(settings).isEmpty());
        QVERIFY(!service.value().remoteCertificate.isEmpty());
        settings.url = "https://other.test:7823"; QVERIFY(service.save(settings).isEmpty());
        QVERIFY(service.value().remoteCertificate.isEmpty());
        settings = service.value(); settings.remoteCertificate = QString::fromUtf8(TlsFixture::certificate().toPem());
        QVERIFY(service.save(settings).isEmpty()); settings.url = "https://other.test:9000";
        QVERIFY(service.save(settings).isEmpty()); QVERIFY(service.value().remoteCertificate.isEmpty());
    }
    void invalidCertificateIsRejectedWithoutChangingSettings() {
        QTemporaryDir dir; SettingsService service(dir.filePath("settings.json"), false);
        auto settings = service.value(); settings.url = "https://example.test"; settings.connectionMode = "remote";
        QVERIFY(service.save(settings).isEmpty()); settings.remoteCertificate = "not a certificate";
        QVERIFY(!service.save(settings).isEmpty()); QVERIFY(service.value().remoteCertificate.isEmpty());
        const auto invalidJson = QJsonDocument(QJsonObject{{"schemaVersion", 1}, {"connectionMode", "remote"},
            {"url", "https://example.test"}, {"remoteCertificate", "invalid"}, {"shareBrowserSignIns", "false"}}).toJson();
        QVERIFY(writeFile(service.path(), invalidJson));
        SettingsService invalid(service.path(), false); QVERIFY(!invalid.loadError().isEmpty());
        QVERIFY(invalid.value().url.isEmpty()); QVERIFY(invalid.value().shareBrowserSignIns);
    }
    void init()
    {
        previousXdg = qgetenv("XDG_CONFIG_HOME");
        previousAppData = qgetenv("APPDATA");
        previousOrganization = QCoreApplication::organizationName();
        previousApplication = QCoreApplication::applicationName();
        QCoreApplication::setOrganizationName("Headroom");
        QCoreApplication::setApplicationName("Headroom");
    }
    void cleanup()
    {
        if (previousXdg.isNull()) qunsetenv("XDG_CONFIG_HOME");
        else qputenv("XDG_CONFIG_HOME", previousXdg);
        if (previousAppData.isNull()) qunsetenv("APPDATA");
        else qputenv("APPDATA", previousAppData);
        QCoreApplication::setOrganizationName(previousOrganization);
        QCoreApplication::setApplicationName(previousApplication);
    }
    void defaultsToLocalAndPreservesOverrides_data()
    {
        QTest::addColumn<int>("platform");
        QTest::newRow("linux") << int(SettingsService::Platform::Linux);
        QTest::newRow("mac") << int(SettingsService::Platform::Mac);
        QTest::newRow("windows") << int(SettingsService::Platform::Windows);
    }

    void macDefaultPathIsStable()
    {
        qunsetenv("XDG_CONFIG_HOME");
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Mac),
                 QDir(QDir::homePath()).filePath(QStringLiteral(".config/headroom/settings.json")));
    }
    void defaultPathsHonorEnvironment()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        qputenv("XDG_CONFIG_HOME", QFile::encodeName(dir.path()));
        qputenv("APPDATA", QFile::encodeName(dir.path()));
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Linux), dir.filePath("headroom/settings.json"));
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Mac), dir.filePath("headroom/settings.json"));
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Windows), dir.filePath("Headroom/settings.json"));
        qputenv("XDG_CONFIG_HOME", "");
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Linux), QDir::homePath() + "/.config/headroom/settings.json");
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Mac), QDir::homePath() + "/.config/headroom/settings.json");
        qunsetenv("XDG_CONFIG_HOME");
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Linux), QDir::homePath() + "/.config/headroom/settings.json");
        qputenv("APPDATA", "");
        const auto roaming = QFileInfo(QFileInfo(QStandardPaths::writableLocation(QStandardPaths::AppDataLocation)).path()).path();
        QCOMPARE(SettingsService::defaultPath(SettingsService::Platform::Windows), QDir(roaming).filePath("Headroom/settings.json"));
    }
    void previousPathsAndInstanceIdentityStayStable()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        qputenv("XDG_CONFIG_HOME", QFile::encodeName(dir.path()));
        QCOMPARE(SettingsService::previousDefaultPath(SettingsService::Platform::Mac),
                 QDir::homePath() + "/Library/Application Support/Headroom/Headroom/settings.json");
        QCOMPARE(SettingsService::previousDefaultPath(SettingsService::Platform::Linux),
                 QDir(QStandardPaths::writableLocation(QStandardPaths::AppConfigLocation)).filePath("settings.json"));
#ifdef Q_OS_LINUX
        QCOMPARE(SettingsService::previousDefaultPath(), dir.filePath("Headroom/Headroom/settings.json"));
#endif
        QCOMPARE(SettingsService::previousDefaultPath(SettingsService::Platform::Windows),
                 QDir(QStandardPaths::writableLocation(QStandardPaths::AppDataLocation)).filePath("settings.json"));
        QCOMPARE(SettingsService::instanceIdentityPath(), SettingsService::previousDefaultPath());
        QVERIFY(SettingsService::instanceIdentityPath() != SettingsService::defaultPath());
    }
    void copiesPreviousSettingsPrivately_data()
    {
        QTest::addColumn<int>("platform");
        QTest::newRow("linux") << int(SettingsService::Platform::Linux);
        QTest::newRow("mac") << int(SettingsService::Platform::Mac);
        QTest::newRow("windows") << int(SettingsService::Platform::Windows);
    }
    void copiesPreviousSettingsPrivately()
    {
        QFETCH(int, platform);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString previous = dir.filePath("previous/settings.json"), target = dir.filePath("headroom/settings.json");
        const QByteArray original = R"({"schemaVersion":1,"connectionMode":"remote","url":"https://example.test","token":"fixture"})";
        QVERIFY(writeFile(previous, original));
        QVERIFY(writeFile(previous + ".bak", "backup"));
        QVERIFY(writeFile(previous + ".legacy.bak", "legacy backup"));
        SettingsService service({}, true, SettingsService::Platform(platform), {}, target, previous);
        QCOMPARE(service.path(), target);
        QVERIFY(service.loadError().isEmpty());
        QVERIFY(service.migrationNotice().isEmpty());
        QCOMPARE(readFile(target), original);
        QCOMPARE(readFile(previous), original);
        QVERIFY(!QFileInfo::exists(target + ".bak"));
        QVERIFY(!QFileInfo::exists(target + ".legacy.bak"));
#ifndef Q_OS_WIN
        const auto publicPermissions = QFileDevice::ReadGroup | QFileDevice::WriteGroup | QFileDevice::ExeGroup |
                                       QFileDevice::ReadOther | QFileDevice::WriteOther | QFileDevice::ExeOther;
        QVERIFY(!(QFile::permissions(target) & publicPermissions));
        QVERIFY(!(QFile::permissions(QFileInfo(target).absolutePath()) & publicPermissions));
        QVERIFY(QFile::permissions(target).testFlag(QFileDevice::ReadOwner));
        QVERIFY(QFile::permissions(target).testFlag(QFileDevice::WriteOwner));
        QVERIFY(!QFile::permissions(target).testFlag(QFileDevice::ExeOwner));
        QVERIFY(QFile::permissions(QFileInfo(target).absolutePath()).testFlag(QFileDevice::ExeOwner));
        QVERIFY(QFile::permissions(QFileInfo(target).absolutePath()).testFlag(QFileDevice::ReadOwner));
        QVERIFY(QFile::permissions(QFileInfo(target).absolutePath()).testFlag(QFileDevice::WriteOwner));
#endif
    }
    void existingNewSettingsWinOverPreviousAndLegacy()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString previous = dir.filePath("previous.json"), target = dir.filePath("new.json"), legacy = dir.filePath("legacy.json");
        const QByteArray current = R"({"schemaVersion":1,"connectionMode":"local","interval":120})";
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local","interval":90})"));
        QVERIFY(writeFile(target, current));
        QVERIFY(writeFile(legacy, R"({"RefreshIntervalSeconds":150})"));
        SettingsService service({}, true, SettingsService::Platform::Windows, legacy, target, previous);
        QCOMPARE(service.value().interval, 120);
        QCOMPARE(readFile(target), current);
        QVERIFY(!service.importedLegacy());
    }
    void concurrentNewSettingsAreNotOverwritten()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString previous = dir.filePath("previous.json"), target = dir.filePath("headroom/settings.json");
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local","interval":90})"));
        const QByteArray concurrent = R"({"schemaVersion":1,"connectionMode":"local","interval":120})";
        SettingsService service({}, true, SettingsService::Platform::Linux, {}, target, previous,
                                [&] { QVERIFY(writeFile(target, concurrent)); });
        QCOMPARE(service.path(), target);
        QCOMPARE(service.value().interval, 120);
        QCOMPARE(readFile(target), concurrent);
        QCOMPARE(QDir(QFileInfo(target).absolutePath()).entryList({".settings-*"}, QDir::Files | QDir::Hidden).size(), 0);
    }
    void copyFailureUsesPreviousSettingsForSaving()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString previous = dir.filePath("previous.json"), blocker = dir.filePath("blocker");
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local","interval":90})"));
        QVERIFY(writeFile(blocker, "not a directory"));
        SettingsService service({}, true, SettingsService::Platform::Linux, {}, blocker + "/settings.json", previous);
        QCOMPARE(service.path(), previous);
        QCOMPARE(service.legacyBackupPath(), previous + ".legacy.bak");
        QVERIFY(service.loadError().isEmpty());
        QVERIFY(!service.migrationNotice().isEmpty());
        auto updated = service.value(); updated.interval = 120;
        QVERIFY(service.save(updated).isEmpty());
        QCOMPARE(readObject(previous).value("interval").toInt(), 120);
    }
    void previousSettingsPreventWindowsLegacyImport()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString previous = dir.filePath("previous.json"), target = dir.filePath("headroom/settings.json"), legacy = dir.filePath("legacy.json");
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local","interval":90})"));
        QVERIFY(writeFile(legacy, R"({"RefreshIntervalSeconds":150})"));
        SettingsService service({}, true, SettingsService::Platform::Windows, legacy, target, previous);
        QCOMPARE(service.value().interval, 90);
        QVERIFY(!service.importedLegacy());
    }
    void defaultsToLocalAndPreservesOverrides()
    {
        QFETCH(int, platform);
        const auto targetPlatform = SettingsService::Platform(platform);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath("settings.json");
        SettingsService fresh(path, true, targetPlatform);
        QCOMPARE(fresh.value().connectionMode, QString("local"));
        QVERIFY(fresh.value().url.isEmpty());
        QVERIFY(fresh.value().token.isEmpty());
        QVERIFY(!QFileInfo::exists(path));

        // A pre-mode settings file with a saved URL is already an override.
        QVERIFY(writeFile(path, QByteArrayLiteral("{\"url\":\"https://usage.example.test/base\",\"token\":\"fixture-token\"}")));
        SettingsService existing(path, true, targetPlatform);
        QCOMPARE(existing.value().connectionMode, QString("remote"));
        QCOMPARE(existing.value().url, QString("https://usage.example.test/base"));
        QCOMPARE(existing.value().token, QString("fixture-token"));
        SettingsService reopened(path, true, targetPlatform);
        QCOMPARE(reopened.value().connectionMode, QString("remote"));
        QCOMPARE(reopened.value().url, existing.value().url);
        QCOMPARE(reopened.value().token, existing.value().token);
    }

    void importsLegacySchemas_data()
    {
        QTest::addColumn<int>("schema");
        for (int schema = 0; schema <= 3; ++schema) QTest::newRow(qPrintable(QString::number(schema))) << schema;
    }
    void importsLegacySchemas()
    {
        QFETCH(int, schema);
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString target = dir.filePath("Headroom/settings.json");
        const QString legacy = dir.filePath("ClaudeUsageWidget/settings.json");
        const QByteArray original = QString(
            "{\"SchemaVersion\":%1,\"ApiUrl\":\"https://example.test/base\",\"ApiToken\":\"fixture-token\",\"RefreshIntervalSeconds\":120,\"StartWithWindows\":true,\"NotificationsEnabled\":false,\"DebugMode\":true,\"PrimaryProvider\":\"Cursor\",\"ProviderOrder\":[\"Codex\",\"Cursor\",\"Claude\"],\"Unknown\":{\"nested\":1}}")
            .arg(schema).toUtf8();
        QVERIFY(writeFile(legacy, original));
        SettingsService service({}, true, SettingsService::Platform::Windows, legacy, target, dir.filePath("absent-previous.json"));
        QVERIFY2(service.loadError().isEmpty(), qPrintable(service.loadError()));
        QVERIFY(service.importedLegacy());
        QCOMPARE(service.value().connectionMode, QString("remote"));
        QCOMPARE(service.value().url, QString("https://example.test/base"));
        QCOMPARE(service.value().token, QString("fixture-token"));
        QCOMPARE(service.value().interval, 120);
        QCOMPARE(service.value().notifications, false);
        QCOMPARE(service.value().order, QStringList({"Codex", "Cursor", "Claude", "Grok"}));
        QCOMPARE(service.value().startup, true);
        QCOMPARE(service.value().startupMigrationPending, true);
        QCOMPARE(readFile(legacy), original);
        QCOMPARE(readFile(service.legacyBackupPath()), original);
#ifndef Q_OS_WIN
        QVERIFY(!(QFile::permissions(service.legacyBackupPath()) &
                  (QFileDevice::ReadGroup | QFileDevice::WriteGroup | QFileDevice::ReadOther | QFileDevice::WriteOther)));
#endif
        QVERIFY(!readFile(target).contains("DebugMode"));
    }

    void localLegacyAndLinuxEmptyMode()
    {
        QTemporaryDir dir;
        const QString legacy = dir.filePath("old.json"), target = dir.filePath("new/settings.json");
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"\",\"ApiToken\":\"keep\",\"PrimaryProvider\":\"Grok\"}")));
        SettingsService windows({}, true, SettingsService::Platform::Windows, legacy, target, dir.filePath("absent-previous.json"));
        QCOMPARE(windows.value().connectionMode, QString("local"));
        QCOMPARE(windows.value().token, QString("keep"));
        QCOMPARE(windows.value().order.first(), QString("Grok"));

        const QString linuxPath = dir.filePath("linux/settings.json");
        QVERIFY(writeFile(linuxPath, QByteArrayLiteral("{\"url\":\"\",\"token\":\"\",\"interval\":60}")));
        SettingsService linuxSettings(linuxPath, true, SettingsService::Platform::Linux);
        QCOMPARE(linuxSettings.value().connectionMode, QString("local"));
        QCOMPARE(readObject(linuxPath).value("connectionMode").toString(), QString("local"));
        QCOMPARE(readFile(linuxPath + ".bak"), QByteArrayLiteral("{\"url\":\"\",\"token\":\"\",\"interval\":60}"));
#ifndef Q_OS_WIN
        QVERIFY(!(QFile::permissions(linuxPath + ".bak") &
                  (QFileDevice::ReadGroup | QFileDevice::WriteGroup | QFileDevice::ReadOther | QFileDevice::WriteOther)));
#endif
    }

    void existingHeadroomWinsAndPreservesUnknown()
    {
        QTemporaryDir dir;
        const QString target = dir.filePath("new.json"), legacy = dir.filePath("old.json");
        QVERIFY(writeFile(target, QByteArrayLiteral("{\"connectionMode\":\"remote\",\"url\":\"https://new.test\",\"token\":\"new-token\",\"order\":[\"Claude\"],\"custom\":{\"x\":2}}")));
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"https://old.test\",\"ApiToken\":\"old-token\"}")));
        SettingsService service({}, true, SettingsService::Platform::Windows, legacy, target);
        QCOMPARE(service.value().url, QString("https://new.test"));
        QVERIFY(!service.importedLegacy());
        auto changed = service.value(); changed.interval = 120;
        QVERIFY(service.save(changed, true).isEmpty());
        QCOMPARE(readObject(target).value("custom").toObject().value("x").toInt(), 2);
        QVERIFY(!QFileInfo::exists(service.legacyBackupPath()));
    }

    void sshRoundTripIsSeparateFromHttpCredentials()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        SettingsService service(path, false, SettingsService::Platform::Linux);
        auto value = service.value();
        value.connectionMode = QStringLiteral("ssh");
        value.url = QStringLiteral("https://usage.example.test/base");
        value.token = QStringLiteral("saved-http-token");
        value.sshUrl = QStringLiteral("ssh://alice@example.test:2222");
        QVERIFY2(service.save(value, true).isEmpty(), qPrintable(service.loadError()));
        SettingsService reopened(path, false, SettingsService::Platform::Linux);
        QCOMPARE(reopened.value().connectionMode, QStringLiteral("ssh"));
        QCOMPARE(reopened.value().sshUrl, value.sshUrl);
        QCOMPARE(reopened.value().url, value.url);
        QCOMPARE(reopened.value().token, value.token);
    }

    void inactiveInvalidSshAddressDoesNotDisableHttpSettings()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        QVERIFY(writeFile(path, QByteArrayLiteral("{\"connectionMode\":\"remote\",\"url\":\"https://usage.example.test\",\"token\":\"keep\",\"sshUrl\":\"ssh://-obad\"}")));
        SettingsService service(path, false, SettingsService::Platform::Linux);
        QVERIFY(service.loadError().isEmpty());
        QCOMPARE(service.value().connectionMode, QStringLiteral("remote"));
        QCOMPARE(service.value().url, QStringLiteral("https://usage.example.test"));
        QCOMPARE(service.value().token, QStringLiteral("keep"));
        QVERIFY(service.value().sshUrl.isEmpty());
    }

    void windowSizeRoundTripsWithoutClobberingSettings()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        QVERIFY(writeFile(path, QByteArrayLiteral("{\"connectionMode\":\"local\",\"custom\":{\"keep\":true}}")));
        SettingsService service(path, false, SettingsService::Platform::Linux);
        QVERIFY(service.saveWindowSize(QSize(1040, 760)).isEmpty());
        SettingsService reopened(path, false, SettingsService::Platform::Linux);
        QCOMPARE(reopened.value().windowSize, QSize(1040, 760));
        QCOMPARE(readObject(path).value("custom").toObject().value("keep").toBool(), true);
        QVERIFY(reopened.saveStartupPreference(true).isEmpty());
        SettingsService afterOtherSave(path, false, SettingsService::Platform::Linux);
        QCOMPARE(afterOtherSave.value().windowSize, QSize(1040, 760));
    }

    void incompleteWindowSizeIsIgnored()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        QVERIFY(writeFile(path, QByteArrayLiteral("{\"windowWidth\":1040}")));
        SettingsService service(path, false, SettingsService::Platform::Linux);
        QVERIFY(!service.value().windowSize.isValid());
    }

    void windowSizeDoesNotDowngradeFutureSettings()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        const QByteArray original = QByteArrayLiteral("{\"schemaVersion\":2,\"windowWidth\":1040,\"windowHeight\":760}");
        QVERIFY(writeFile(path, original));
        SettingsService service(path, false, SettingsService::Platform::Linux);
        QVERIFY(!service.loadError().isEmpty());
        QVERIFY(!service.saveWindowSize(QSize(900, 700)).isEmpty());
        QCOMPARE(readFile(path), original);
    }

    void rejectsInvalidActiveSshAddressWithoutChangingFile()
    {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        const QString path = dir.filePath(QStringLiteral("settings.json"));
        const QByteArray original = QByteArrayLiteral("{\"connectionMode\":\"ssh\",\"sshUrl\":\"ssh://user:password@example.test\"}");
        QVERIFY(writeFile(path, original));
        SettingsService service(path, true, SettingsService::Platform::Linux);
        QVERIFY(!service.loadError().isEmpty());
        QCOMPARE(readFile(path), original);
    }

    void explicitConfigIsIsolated()
    {
        QTemporaryDir dir;
        const QString explicitPath = dir.filePath("isolated/settings.json");
        const QString defaultPath = dir.filePath("default/settings.json");
        const QString legacy = dir.filePath("old.json");
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"https://legacy.test\"}")));
        const QString previous = dir.filePath("previous.json");
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local"})"));
        SettingsService service(explicitPath, true, SettingsService::Platform::Windows, legacy, defaultPath, previous);
        QVERIFY(!service.importedLegacy());
        QVERIFY(!QFileInfo::exists(explicitPath));
        QVERIFY(!QFileInfo::exists(defaultPath));
        QVERIFY(!QFileInfo::exists(defaultPath + ".legacy.bak"));
    }

    void automaticMigrationCanBeDisabledForCapture()
    {
        QTemporaryDir dir;
        const QString target = dir.filePath("default/settings.json"), legacy = dir.filePath("old.json");
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"https://legacy.test\"}")));
        const QString previous = dir.filePath("previous.json");
        QVERIFY(writeFile(previous, R"({"schemaVersion":1,"connectionMode":"local"})"));
        SettingsService absent({}, false, SettingsService::Platform::Windows, legacy, target, previous);
        QVERIFY(!absent.importedLegacy());
        QVERIFY(!QFileInfo::exists(target));
        QVERIFY(!QFileInfo::exists(target + ".legacy.bak"));

        QVERIFY(writeFile(target, QByteArrayLiteral("{\"url\":\"\",\"token\":\"\"}")));
        const QByteArray original = readFile(target);
        SettingsService existing({}, false, SettingsService::Platform::Windows, legacy, target);
        QCOMPARE(existing.value().connectionMode, QString("local"));
        QCOMPARE(readFile(target), original);
        QVERIFY(!QFileInfo::exists(target + ".bak"));
    }

    void malformedAndInvalidFilesStayUntouched()
    {
        QTemporaryDir dir;
        for (const QByteArray original : {QByteArray("{broken"),
             QByteArray("{\"connectionMode\":\"remote\",\"url\":\"https://user:pass@example.test\",\"token\":\"x\"}"),
             QByteArray("{\"connectionMode\":\"remote\",\"url\":\"https://example.test\",\"token\":\"line\\nbreak\"}")}) {
            const QString path = dir.filePath(QString::number(qHash(original)) + ".json");
            QVERIFY(writeFile(path, original));
            SettingsService service(path, true, SettingsService::Platform::Linux);
            QVERIFY(!service.loadError().isEmpty());
            QVERIFY(!service.saveWindowSize(QSize(900, 700)).isEmpty());
            QCOMPARE(readFile(path), original);
            QVERIFY(!service.saveOrder({"Grok"}, "Grok").isEmpty());
            QCOMPARE(readFile(path), original);
            auto replacement = service.value(); replacement.connectionMode = "remote";
            replacement.url = "https://replacement.test"; replacement.token.clear();
            QVERIFY(service.save(replacement, true).isEmpty());
            QVERIFY(readFile(path) != original);
        }
    }

    void failedImportIsRetryableAndBackupIsNeverOverwritten()
    {
        QTemporaryDir dir;
        const QString target = dir.filePath("new/settings.json"), legacy = dir.filePath("old.json");
        const QByteArray malformed = "{bad"; QVERIFY(writeFile(legacy, malformed));
        SettingsService failed({}, true, SettingsService::Platform::Windows, legacy, target, dir.filePath("absent-previous.json"));
        QVERIFY(!failed.loadError().isEmpty());
        QVERIFY(!failed.saveOrder({"Grok"}, "Grok").isEmpty());
        QVERIFY(!QFileInfo::exists(target));
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"\",\"ApiToken\":\"token\"}")));
        SettingsService imported({}, true, SettingsService::Platform::Windows, legacy, target, dir.filePath("absent-previous.json"));
        QVERIFY(imported.importedLegacy());
        const QByteArray backup = readFile(imported.legacyBackupPath());
        QVERIFY(writeFile(legacy, QByteArrayLiteral("{\"ApiUrl\":\"https://changed.test\"}")));
        QFile::remove(target);
        SettingsService retry({}, true, SettingsService::Platform::Windows, legacy, target, dir.filePath("absent-previous.json"));
        QVERIFY(retry.importedLegacy());
        QCOMPARE(readFile(retry.legacyBackupPath()), backup);
    }
};

QTEST_GUILESS_MAIN(SettingsTest)
#include "test_settings.moc"
