#include "updateservice.h"
#include "controller.h"
#include <QCoreApplication>
#include <QFile>
#include <QDir>
#include <QFileInfo>
#include <QTemporaryDir>
#include <QtTest>

class UpdateServiceTest : public QObject {
    Q_OBJECT
private:
    QTemporaryDir m_dir;
    QString m_record;
    UpdateServiceOptions options() const {
        UpdateServiceOptions value;
        const QString root = QFileInfo(m_dir.path()).canonicalFilePath();
        value.managerPath = QStringLiteral(UPDATE_FIXTURE_PATH);
        value.installRoot = root;
        value.launcherPath = QDir(root).filePath(QStringLiteral("headroom"));
        value.packageVersion = QStringLiteral("0.1.0");
        value.timeoutMs = 1000;
        value.cancelGraceMs = 100;
        value.fixtureIdentity = true;
        return value;
    }
    QByteArray record() const { QFile file(m_record); return file.open(QIODevice::ReadOnly) ? file.readAll() : QByteArray(); }
    QJsonObject cliRequest() const {
        return {{"schema", 1}, {"product", "Headroom"}, {"command", "update"},
            {"install_root", options().installRoot}, {"request_nonce", QString(48, QLatin1Char('b'))},
            {"additional_processes", QJsonArray{QJsonObject{{"role", "cli"}, {"pid", 12345},
                {"executable", QCoreApplication::applicationFilePath()}}}}};
    }
private slots:
    void initTestCase() { QVERIFY(m_dir.isValid()); m_record = m_dir.filePath(QStringLiteral("record")); QCoreApplication::setApplicationVersion(QStringLiteral("0.1.0")); }
    void init() {
        qunsetenv("HEADROOM_UPDATE_FIXTURE_ERROR");
        QFile::remove(m_record);
        QDir(m_dir.filePath(QStringLiteral("transactions"))).removeRecursively();
        QDir(m_dir.filePath(QStringLiteral("staging"))).removeRecursively();
        QDir(m_dir.filePath(QStringLiteral("pairing"))).removeRecursively();
        qunsetenv("HEADROOM_UPDATE_FIXTURE_CLI");
        qputenv("HEADROOM_UPDATE_FIXTURE_RECORD", m_record.toUtf8());
		qunsetenv("HEADROOM_UPDATE_FIXTURE_MODE"); qunsetenv("HEADROOM_UPDATE_FIXTURE_MISSING"); qunsetenv("HEADROOM_UPDATE_FIXTURE_INTERNAL_LAUNCHER"); qunsetenv("HEADROOM_UPDATE_FIXTURE_BAD_STAGE"); qunsetenv("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS");
    }
	void cleanup() { qunsetenv("HEADROOM_UPDATE_FIXTURE_RECORD"); qunsetenv("HEADROOM_UPDATE_FIXTURE_MODE"); qunsetenv("HEADROOM_UPDATE_FIXTURE_MISSING"); qunsetenv("HEADROOM_UPDATE_FIXTURE_INTERNAL_LAUNCHER"); qunsetenv("HEADROOM_UPDATE_FIXTURE_BAD_STAGE"); qunsetenv("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS"); qunsetenv("USAGE_AUTH_TOKEN"); }
    void updateLifecycleAppearsInDesktopDiagnostics() {
        CredentialServiceOptions credentials; credentials.enabled = false;
        Controller controller(m_dir.filePath("diagnostics-settings.json"), nullptr, false, {}, credentials, {}, false);
        auto config = options();
        config.diagnostic = [&controller](const QString &message) { controller.logUpdate(message); };
        UpdateService service(true, config);
        // The sink must be installed before construction to capture inspection.
        QVERIFY(controller.diagnosticText().contains("inspect: Started."));
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        const auto diagnostics = controller.diagnosticText();
        QVERIFY(diagnostics.contains("[update]"));
        QVERIFY(diagnostics.contains("check-update: Started."));
        QVERIFY(diagnostics.contains("Check completed: a newer package is available."));
        QVERIFY(diagnostics.contains("stage-update: Started."));
        QVERIFY(diagnostics.contains("Download and verification completed"));
        QVERIFY(!diagnostics.contains(m_dir.path()));
    }
    void failedChecksExplainSafeCause_data() {
        QTest::addColumn<QString>("error");
        QTest::addColumn<QString>("expected");
        QTest::newRow("renamed-repository") << "release server returned HTTP 301" << "current Headroom installer";
        QTest::newRow("forbidden") << "release server returned HTTP 403" << "HTTP 403";
        QTest::newRow("rate-limited") << "release server returned HTTP 429" << "rate limited";
        QTest::newRow("unavailable") << "release server returned HTTP 503" << "HTTP 503";
        QTest::newRow("dns") << "download release data: Get https://example.test/?token=private-token: no such host" << "DNS";
        QTest::newRow("tls") << "download release data: Get https://example.test/?token=private-token: x509: certificate signed by unknown authority" << "TLS";
        QTest::newRow("timeout") << "download release data: Get https://example.test/?token=private-token: context deadline exceeded" << "timed out";
        QTest::newRow("proxy") << "download release data: Get https://user:private-token@example.test/: proxyconnect tcp: connection refused" << "proxy settings";
        QTest::newRow("integrity") << "downloaded package checksum does not match release manifest" << "integrity verification";
        QTest::newRow("unknown-private-error") << "private-token and https://example.test/private" << "exit code 2";
    }
    void failedChecksExplainSafeCause() {
        QFETCH(QString, error); QFETCH(QString, expected);
        QStringList events;
        auto config = options(); config.diagnostic = [&](const QString &message) { events.append(message); };
        UpdateService service(true, config); QTRY_VERIFY(service.canCheck());
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "error");
        qputenv("HEADROOM_UPDATE_FIXTURE_ERROR", error.toUtf8());
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QString("failed"));
        QVERIFY(service.statusText().contains(expected));
        const auto diagnostics = events.join('\n');
        QVERIFY(diagnostics.contains("check-update: Started."));
        QVERIFY(diagnostics.contains("Package service exited with code 2."));
        QVERIFY(diagnostics.contains(expected));
        QVERIFY(!service.restartAvailable());
        for (const auto &output : {service.statusText(), diagnostics}) {
            QVERIFY(!output.contains("private-token"));
            QVERIFY(!output.contains("private-stderr-value"));
            QVERIFY(!output.contains("https://"));
        }
        qunsetenv("HEADROOM_UPDATE_FIXTURE_ERROR");
    }
    void failedProcessStartIsDiagnosed() {
        QStringList events;
        auto config = options(); config.managerPath = m_dir.filePath("absent-package-manager");
        config.diagnostic = [&](const QString &message) { events.append(message); };
        UpdateService service(true, config);
        QTRY_COMPARE(service.state(), QString("failed"));
        QVERIFY(events.join('\n').contains("package service could not be started"));
        QVERIFY(!events.join('\n').contains(m_dir.path()));
    }
    void sourceAndIsolatedModesNeverStartManager() {
        UpdateServiceOptions source;
        source.managerPath = QStringLiteral(UPDATE_FIXTURE_PATH);
        UpdateService service(true, source);
        QCOMPARE(service.state(), QStringLiteral("unavailable")); QCOMPARE(service.updateMethod(), QStringLiteral("source"));
        QCOMPARE(service.statusText(), QStringLiteral("This source installation is updated from its source checkout."));
        service.startAutomaticCheck(); service.checkForUpdates(); QTest::qWait(50);
        QVERIFY(record().isEmpty());
        auto official = options(); UpdateService isolated(false, official);
        isolated.startAutomaticCheck(); QTest::qWait(50);
        QVERIFY(record().isEmpty()); QVERIFY(isolated.statusText().contains(QStringLiteral("disabled")));
        official.systemManaged = true; UpdateService system(true, official); system.startAutomaticCheck(); QTest::qWait(50);
        QVERIFY(record().isEmpty()); QCOMPARE(system.updateMethod(), QStringLiteral("system"));
        QCOMPARE(system.statusText(), QStringLiteral("This installation is managed by your system package manager."));
    }
    void desktopUpdateAcceptsOnlyItsManagersRegisteredStandaloneServer() {
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "managed-server");
        UpdateService service(true, options());
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        service.restartToApply(); QTRY_COMPARE(prepared.count(), 1);
    }
    void cliPreparedStageIsAppliedWithoutResolvingAnotherRelease() {
        UpdateService service(true, options());
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        const auto before = record();
        auto request = cliRequest(); request["prepared_stage"] = service.verifiedStage();
        QJsonObject reply;
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        service.requestCLIUpdate(request, [&](const QJsonObject &value) { reply = value; });
        QTRY_COMPARE(prepared.count(), 1);
        QVERIFY(reply.value("ok").toBool());
        const auto added = record().mid(before.size());
        QVERIFY(added.contains("prepare-apply"));
        QVERIFY(!added.contains("check-update")); QVERIFY(!added.contains("stage-update"));
    }
    void cliPreparedStageCannotChangePackageProfile() {
        UpdateService service(true, options());
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        auto stage = service.verifiedStage(); stage["package_kind"] = "cli";
        const QString path = QDir(stage.value("package_root").toString()).absoluteFilePath("../../verified-stage.json");
        QFile stagedRecord(path); QVERIFY(stagedRecord.open(QIODevice::WriteOnly | QIODevice::Truncate));
        stagedRecord.write(QJsonDocument(stage).toJson()); stagedRecord.close();
        auto request = cliRequest(); request["prepared_stage"] = stage;
        QJsonObject reply;
        service.requestCLIUpdate(request, [&](const QJsonObject &value) { reply = value; });
        QCOMPARE(reply.value("ok").toBool(), false);
        QCOMPARE(service.state(), QString("failed"));
        QVERIFY(!record().contains("prepare-apply"));
    }
    void pendingOrInvalidPairingRetainsVerifiedStage() {
        for (const auto &body : {QByteArray(R"({"schema":1,"product":"Headroom","state":"pending"})"), QByteArray("not json"), QByteArray("{}")}) {
            UpdateService service(true, options());
            QTRY_VERIFY(service.canCheck());
            service.checkForUpdates(); QTRY_VERIFY(service.canStage());
            service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
            const auto stage = service.verifiedStage();
            const auto before = record();
            QVERIFY(QDir().mkpath(m_dir.filePath("pairing")));
            QFile pair(m_dir.filePath("pairing/windows-wsl.json")); QVERIFY(pair.open(QIODevice::WriteOnly | QIODevice::Truncate)); pair.write(body); pair.close();
            QSignalSpy prepared(&service, &UpdateService::applyPrepared);
            service.restartToApply();
            QVERIFY(!service.busy()); QVERIFY(service.restartAvailable());
            QCOMPARE(service.verifiedStage(), stage); QCOMPARE(record(), before);
            QCOMPARE(prepared.count(), 0);
            QVERIFY(service.statusText().contains("pairing needs attention"));
            QVERIFY(service.statusText().contains("--this-install-only"));
        }
    }
    void failedPairingCoordinatorRetainsVerifiedStage() {
        const QString directory = m_dir.filePath("paired runtime");
        QVERIFY(QDir().mkpath(directory));
#ifdef Q_OS_WIN
        const QString executable = QDir(directory).filePath("headroom-cli.exe");
#else
        const QString executable = QDir(directory).filePath("headroom-cli");
#endif
        QFile::remove(executable);
        QVERIFY(QFile::copy(QStringLiteral(UPDATE_FIXTURE_PATH), executable));
        qputenv("HEADROOM_UPDATE_FIXTURE_CLI", executable.toUtf8());
        QVERIFY(QDir().mkpath(m_dir.filePath("pairing")));
        QFile pair(m_dir.filePath("pairing/windows-wsl.json")); QVERIFY(pair.open(QIODevice::WriteOnly)); pair.write(R"({"schema":1,"product":"Headroom","state":"active"})"); pair.close();
        auto value = options(); value.applicationPath = QDir(directory).filePath("headroom");
        qputenv("USAGE_AUTH_TOKEN", "must-not-reach-paired-coordinator");
        UpdateService service(true, value);
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        service.restartToApply();
        QTRY_VERIFY(service.busy());
        QProcess *coordinator = nullptr;
        for (auto process : service.findChildren<QProcess *>()) if (process->program() == executable) coordinator = process;
        QVERIFY(coordinator); QTRY_VERIFY(coordinator->processId() > 0);
        // The paired coordinator is public update tooling, so it never inherits the
        // server bearer token. The leading newline keeps this off the check-update
        // and stage-update lines, which also end in "update token=".
        QTRY_VERIFY(record().contains("\nupdate token="));
        QVERIFY(record().contains("\nupdate token=absent"));
        QVERIFY(!record().contains("\nupdate token=present"));
        const auto stage = service.verifiedStage();
        coordinator->kill();
        QTRY_VERIFY(!service.busy());
        QVERIFY(service.restartAvailable());
        QCOMPARE(service.verifiedStage(), stage);
        QVERIFY(service.statusText().contains("downloaded package is still available"));
    }
    void pairedUpdateOnlyAcceptsItsOwnCoordinator() {
        const QString directory = m_dir.filePath("paired runtime");
        QVERIFY(QDir().mkpath(directory));
#ifdef Q_OS_WIN
        const QString executable = QDir(directory).filePath("headroom-cli.exe");
#else
        const QString executable = QDir(directory).filePath("headroom-cli");
#endif
        QFile::remove(executable);
        QVERIFY(QFile::copy(QStringLiteral(UPDATE_FIXTURE_PATH), executable));
        qputenv("HEADROOM_UPDATE_FIXTURE_CLI", executable.toUtf8());
        QVERIFY(QDir().mkpath(m_dir.filePath("pairing")));
        QFile pair(m_dir.filePath("pairing/windows-wsl.json")); QVERIFY(pair.open(QIODevice::WriteOnly)); pair.write(R"({"schema":1,"product":"Headroom","state":"active"})"); pair.close();
        auto value = options(); value.applicationPath = QDir(directory).filePath("headroom");
        qputenv("USAGE_AUTH_TOKEN", "must-not-reach-paired-coordinator");
        UpdateService service(true, value);
        QTRY_VERIFY(service.canCheck());
        service.checkForUpdates(); QTRY_VERIFY(service.canStage());
        service.stageUpdate(); QTRY_VERIFY(service.restartAvailable());
        service.restartToApply();
        QTRY_VERIFY(service.busy());
        QProcess *coordinator = nullptr;
        for (auto process : service.findChildren<QProcess *>()) if (process->program() == executable) coordinator = process;
        QVERIFY(coordinator); QTRY_VERIFY(coordinator->processId() > 0);
        // The paired coordinator is public update tooling, so it never inherits the
        // server bearer token. The leading newline keeps this off the check-update
        // and stage-update lines, which also end in "update token=".
        QTRY_VERIFY(record().contains("\nupdate token="));
        QVERIFY(record().contains("\nupdate token=absent"));
        QVERIFY(!record().contains("\nupdate token=present"));
        QJsonObject reply;
        service.requestCLIUpdate(cliRequest(), [&](const QJsonObject &result) { reply = result; });
        QCOMPARE(reply.value("status").toString(), QString("busy"));
        QVERIFY(!record().contains("prepare-apply"));
        auto request = cliRequest();
        request["additional_processes"] = QJsonArray{QJsonObject{{"role", "cli"}, {"pid", coordinator->processId()}, {"executable", executable}}};
        request["prepared_stage"] = service.verifiedStage();
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        service.requestCLIUpdate(request, [&](const QJsonObject &result) { reply = result; });
        QTRY_COMPARE(prepared.count(), 1); QVERIFY(reply.value("ok").toBool());
    }
    void cliRequestIsRejectedForUnmanagedOrDifferentInstall() {
        UpdateService isolated(false, options());
        QJsonObject result;
        isolated.requestCLIUpdate(cliRequest(), [&](const QJsonObject &reply) { result = reply; });
        QCOMPARE(result.value("status").toString(), QString("unmanaged_installation"));
        QVERIFY(record().isEmpty());
        UpdateService service(true, options());
        QTRY_VERIFY(service.canCheck());
        const auto before = record();
        auto request = cliRequest(); request["install_root"] = QDir(options().installRoot).filePath("other");
        service.requestCLIUpdate(request, [&](const QJsonObject &reply) { result = reply; });
        QCOMPARE(result.value("status").toString(), QString("invalid_request"));
        QCOMPARE(record(), before);
    }
    void cliUpdateAcknowledgesOnlyAfterVerifiedCommit() {
        UpdateService service(true, options());
        QTRY_VERIFY(service.canCheck());
        QStringList events;
        connect(&service, &UpdateService::applyPrepared, this, [&] { events.append("quit"); });
        service.requestCLIUpdate(cliRequest(), [&](const QJsonObject &reply) {
            QCOMPARE(reply.value("status").toString(), QString("accepted"));
            QVERIFY(reply.value("ok").toBool());
            QCOMPARE(reply.value("request_nonce"), cliRequest().value("request_nonce"));
            QVERIFY(QFileInfo::exists(m_dir.filePath("transactions/apply-0123456789abcdef0123456789abcdef/commit.json")));
            events.append("reply");
        });
        QTRY_COMPARE(events, QStringList({"reply", "quit"}));
        QVERIFY(record().contains("--participant"));
    }
    void cliUpdateRejectsChangedParticipantBeforeCommit() {
        UpdateService service(true, options());
        QTRY_VERIFY(service.canCheck());
        QJsonObject result;
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "mismatched-participant");
        // Stage normally, then corrupt only the manager's captured identity.
        service.requestCLIUpdate(cliRequest(), [&](const QJsonObject &reply) { result = reply; });
        QTRY_VERIFY(!result.isEmpty());
        QVERIFY(!result.value("ok").toBool());
        QVERIFY(record().contains("prepare-apply"));
        QVERIFY(!QFileInfo::exists(m_dir.filePath("transactions/apply-0123456789abcdef0123456789abcdef/commit.json")));
    }
    void externalStableEntryIsValidatedWithoutPathEquality() {
        const QString rootAlias = m_dir.filePath(QStringLiteral("native identity"));
        QVERIFY(QDir().mkpath(rootAlias));
        const QString root = QFileInfo(rootAlias).canonicalFilePath();
#ifdef Q_OS_WIN
        const QString relativeApplication = QStringLiteral("bin/headroom.exe");
        const QString internalName = QStringLiteral("headroom.exe");
        const QString external = m_dir.filePath(QStringLiteral("custom entry.exe"));
#elif defined(Q_OS_MACOS)
        const QString relativeApplication = QStringLiteral("Headroom.app/Contents/MacOS/headroom");
        const QString internalName = QStringLiteral("headroom-launcher");
        const QString external = m_dir.filePath(QStringLiteral("custom entry"));
#else
        const QString relativeApplication = QStringLiteral("bin/headroom");
        const QString internalName = QStringLiteral("headroom-launcher");
        const QString external = m_dir.filePath(QStringLiteral("custom entry"));
#endif
        const QString application = QDir(root).filePath(QStringLiteral("versions/0.1.0/") + relativeApplication);
        const QString internal = QDir(root).filePath(internalName);
        QVERIFY(QDir().mkpath(QFileInfo(application).absolutePath()));
        auto write = [](const QString &path, const QByteArray &data) { QFile file(path); return file.open(QIODevice::WriteOnly) && file.write(data) == data.size(); };
        QVERIFY(write(application, "application")); QVERIFY(write(internal, "stable launcher")); QVERIFY(write(external, "stable launcher"));
        QVERIFY(write(external + QStringLiteral(".root"), QDir::toNativeSeparators(root).toUtf8() + '\n'));
        qputenv("HEADROOM_UPDATE_FIXTURE_INTERNAL_LAUNCHER", QDir::toNativeSeparators(internal).toUtf8());
        auto value = options(); value.installRoot = QDir::toNativeSeparators(root); value.launcherPath = QDir::toNativeSeparators(external);
        value.applicationPath = QDir::toNativeSeparators(application); value.fixtureIdentity = false;
        UpdateService service(true, value);
        QTRY_COMPARE(service.state(), QStringLiteral("current")); QCOMPARE(service.updateMethod(), QStringLiteral("automatic"));
    }
    void damagedOfficialLauncherRequestsExternalInstallerRepair() {
        const QString rootAlias = m_dir.filePath(QStringLiteral("damaged official identity"));
        QVERIFY(QDir().mkpath(rootAlias));
        const QString root = QFileInfo(rootAlias).canonicalFilePath();
#ifdef Q_OS_WIN
        const QString relativeApplication = QStringLiteral("bin/headroom.exe");
        const QString external = m_dir.filePath(QStringLiteral("damaged entry.exe"));
#elif defined(Q_OS_MACOS)
        const QString relativeApplication = QStringLiteral("Headroom.app/Contents/MacOS/headroom");
        const QString external = m_dir.filePath(QStringLiteral("damaged entry"));
#else
        const QString relativeApplication = QStringLiteral("bin/headroom");
        const QString external = m_dir.filePath(QStringLiteral("damaged entry"));
#endif
        const QString application = QDir(root).filePath(QStringLiteral("versions/0.1.0/") + relativeApplication);
        QVERIFY(QDir().mkpath(QFileInfo(application).absolutePath()));
        QFile app(application); QVERIFY(app.open(QIODevice::WriteOnly)); QVERIFY(app.write("application") > 0); app.close();
        auto value = options(); value.installRoot = QDir::toNativeSeparators(root); value.launcherPath = QDir::toNativeSeparators(external);
        value.applicationPath = QDir::toNativeSeparators(application); value.fixtureIdentity = false;
        UpdateService service(true, value);
        QTRY_COMPARE(service.state(), QStringLiteral("unavailable"));
        QVERIFY(service.statusText().contains(QStringLiteral("official installer")));
        QVERIFY(!service.canRepair());
    }
    void manualCheckAndStageUseExplicitStatesAndNoBearerEnvironment() {
        qputenv("USAGE_AUTH_TOKEN", "must-not-reach-public-updater");
        UpdateService service(true, options());
        QTRY_COMPARE(service.state(), QStringLiteral("current"));
        QVERIFY(service.canCheck()); service.checkForUpdates();
        QTRY_COMPARE(service.state(), QStringLiteral("available"));
        QCOMPARE(service.latestVersion(), QStringLiteral("9.1.0")); QVERIFY(service.canStage());
        service.stageUpdate(); QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QVERIFY(service.restartAvailable()); QVERIFY(service.statusText().contains(QStringLiteral("Restart")));
        const auto calls = record(); QVERIFY(calls.contains("inspect token=absent")); QVERIFY(calls.contains("check-update token=absent")); QVERIFY(calls.contains("stage-update token=absent"));
    }
    void automaticCheckReusesVerifiedStage() {
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "staged");
        UpdateService service(true, options());
        service.startAutomaticCheck();
        QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QVERIFY(service.restartAvailable());
        QCOMPARE(record().count("check-update"), 1);
        QVERIFY(!record().contains("stage-update"));
    }
    void automaticCheckStagesOnce() {
        UpdateService service(true, options());
        service.startAutomaticCheck();
        QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QCOMPARE(record().count("check-update"), 1); QCOMPARE(record().count("stage-update"), 1);
    }
    void previewBeforeStartupTimerConsumesAutomaticCheckWithoutTraffic() {
        UpdateService service(true, options());
        QTRY_COMPARE(service.state(), QStringLiteral("current"));
        service.setPublicTrafficAllowed(false);
        QVERIFY(!service.canCheck()); QVERIFY(!service.canRepair()); QVERIFY(!service.restartAvailable());
        service.startAutomaticCheck(); QTest::qWait(100);
        QVERIFY(!record().contains("check-update")); QVERIFY(!record().contains("stage-update"));
        service.setPublicTrafficAllowed(true); QTRY_VERIFY(service.canCheck()); QTest::qWait(100);
        QVERIFY(!record().contains("check-update"));
        service.startAutomaticCheck(); QTest::qWait(100); QVERIFY(!record().contains("check-update"));
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
        service.setPublicTrafficAllowed(false); QVERIFY(!service.canStage()); QCOMPARE(service.state(), QStringLiteral("unavailable"));
        service.setPublicTrafficAllowed(true); QCOMPARE(service.state(), QStringLiteral("available")); QVERIFY(service.canStage());
    }
    void rapidPreviewOffOnWaitsForCancelledOperationThenRestoresManualOnly() {
        UpdateService service(true, options()); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "hang"); service.checkForUpdates(); QTRY_VERIFY(service.busy());
        service.setPublicTrafficAllowed(false); service.setPublicTrafficAllowed(true);
        QTRY_COMPARE(service.state(), QStringLiteral("current")); QVERIFY(service.canCheck());
        QCOMPARE(record().count("check-update"), 1); QCOMPARE(record().count("stage-update"), 0);
    }
    void matchingRepairStagesCurrentVersion() {
        qputenv("HEADROOM_UPDATE_FIXTURE_MISSING", "1");
        UpdateService service(true, options());
        QTRY_VERIFY(service.canRepair()); QVERIFY(service.statusText().contains(QStringLiteral("needs repair")));
        service.repairInstallation(); QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QCOMPARE(service.latestVersion(), QStringLiteral("0.1.0")); QVERIFY(record().contains("stage-repair"));
        // Local repair must remain available even when paired identity is incomplete.
        QVERIFY(QDir().mkpath(m_dir.filePath("pairing")));
        QFile pair(m_dir.filePath("pairing/windows-wsl.json")); QVERIFY(pair.open(QIODevice::WriteOnly)); pair.write("{}"); pair.close();
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        service.restartToApply(); QTRY_COMPARE(prepared.count(), 1);
        QVERIFY(record().contains("prepare-apply"));
    }
    void bootstrapDamageIsRepairableForVerifiedIdentity() {
        qputenv("HEADROOM_UPDATE_FIXTURE_MISSING", "bootstrap/headroom-package");
        UpdateService service(true, options());
        QTRY_VERIFY(service.canRepair()); QVERIFY(service.statusText().contains(QStringLiteral("installation needs repair")));
    }
    void cancellationNeverAdvertisesRestart() {
        QStringList events;
        auto config = options(); config.diagnostic = [&](const QString &message) { events.append(message); };
        UpdateService service(true, config); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "hang");
        service.checkForUpdates(); QTRY_VERIFY(service.busy()); service.cancel();
        QTRY_COMPARE(service.state(), QStringLiteral("failed")); QVERIFY(!service.restartAvailable());
        QVERIFY(events.join('\n').contains("Cancellation requested."));
        QVERIFY(events.join('\n').contains("operation was cancelled."));
    }
    void malformedToolOutputFailsClosed() {
        UpdateService service(true, options()); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "malformed"); service.checkForUpdates();
        QTRY_COMPARE(service.state(), QStringLiteral("failed")); QVERIFY(!service.restartAvailable());
    }
    void timeoutAndOversizedStderrFailClosed() {
        auto shortOptions = options(); shortOptions.timeoutMs = 500;
        QStringList events;
        shortOptions.diagnostic = [&](const QString &message) { events.append(message); };
        UpdateService timed(true, shortOptions); QTRY_COMPARE(timed.state(), QStringLiteral("current"));
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "hang"); timed.checkForUpdates();
        QTRY_COMPARE(timed.state(), QStringLiteral("failed")); QVERIFY(timed.statusText().contains(QStringLiteral("timed out")));
        QVERIFY(events.join('\n').contains("timed out"));
        qunsetenv("HEADROOM_UPDATE_FIXTURE_MODE");
        UpdateService noisy(true, options()); QTRY_COMPARE(noisy.state(), QStringLiteral("current"));
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "stderr"); noisy.checkForUpdates();
        QTRY_COMPARE(noisy.state(), QStringLiteral("failed")); QVERIFY(!noisy.restartAvailable());
    }
    void inconsistentVerifiedStageIsNeverAdvertised() {
        UpdateService service(true, options()); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
        qputenv("HEADROOM_UPDATE_FIXTURE_BAD_STAGE", "1"); service.stageUpdate();
        QTRY_COMPARE(service.state(), QStringLiteral("failed")); QVERIFY(!service.restartAvailable()); QVERIFY(service.verifiedStage().isEmpty());
    }
    void restartRequiresValidatedCommitAndCannotBeCancelled() {
        UpdateService service(true, options()); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        service.setRelaunchArguments({QStringLiteral("--background")});
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
        service.stageUpdate(); QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        service.restartToApply();
		QVERIFY(service.busy()); QVERIFY(!service.canCancel());
        service.cancel(); service.setPublicTrafficAllowed(false);
		QTRY_VERIFY2(prepared.count() == 1, qPrintable(service.statusText()));
        QCOMPARE(service.state(), QStringLiteral("applying"));
        const QString commit = m_dir.filePath(QStringLiteral("transactions/apply-0123456789abcdef0123456789abcdef/commit.json"));
        QFile commitFile(commit); QVERIFY(commitFile.open(QIODevice::ReadOnly));
        const auto committed = QJsonDocument::fromJson(commitFile.readAll()).object();
        QVERIFY(committed.value(QStringLiteral("commit")).toBool());
        QCOMPARE(committed.value(QStringLiteral("nonce")).toString(), QString(48, QLatin1Char('a')));
        QVERIFY(record().contains("--relaunch-arg|--background"));
    }
    void mismatchedPreparedApplyDoesNotCommitOrClose() {
        UpdateService service(true, options()); QTRY_COMPARE(service.state(), QStringLiteral("current"));
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
        service.stageUpdate(); QTRY_COMPARE(service.state(), QStringLiteral("staged"));
        QSignalSpy prepared(&service, &UpdateService::applyPrepared);
        qputenv("HEADROOM_UPDATE_FIXTURE_MODE", "mismatched-prepare");
        service.restartToApply();
        QTRY_COMPARE(service.state(), QStringLiteral("failed"));
        QCOMPARE(prepared.count(), 0);
        const QString commit = m_dir.filePath(QStringLiteral("transactions/apply-0123456789abcdef0123456789abcdef/commit.json"));
        QVERIFY(!QFileInfo::exists(commit));
    }
    void rollbackOutcomeSuppressesAutomaticRestageUntilManualCheck() {
        qputenv("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS", "rolled_back");
        UpdateService service(true, options()); service.startAutomaticCheck(); QTRY_COMPARE(service.state(), QStringLiteral("failed"));
        QVERIFY(service.statusText().contains(QStringLiteral("restored")));
        QTest::qWait(100);
        QCOMPARE(record().count("check-update"), 0);
        service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
        QCOMPARE(record().count("check-update"), 1);
    }
    void recoveryRequiredSuppressesAutomaticRestage() {
        qputenv("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS", "recovery_required");
        UpdateService service(true, options()); service.startAutomaticCheck();
        QTRY_COMPARE(service.state(), QStringLiteral("failed"));
        QVERIFY(service.statusText().contains(QStringLiteral("retry recovery")));
        QTest::qWait(100); QCOMPARE(record().count("check-update"), 0);
    }
    void failedApplyPreparationAppliesDeferredPreviewState() {
        auto value = options(); value.timeoutMs = 1000;
        for (const auto &mode : {QByteArray("malformed"), QByteArray("hang")}) {
            qunsetenv("HEADROOM_UPDATE_FIXTURE_MODE");
            UpdateService service(true, value); QTRY_COMPARE(service.state(), QStringLiteral("current"));
            service.checkForUpdates(); QTRY_COMPARE(service.state(), QStringLiteral("available"));
            service.stageUpdate(); QTRY_COMPARE(service.state(), QStringLiteral("staged"));
            qputenv("HEADROOM_UPDATE_FIXTURE_MODE", mode);
            service.restartToApply(); QVERIFY(service.busy()); QVERIFY(!service.canCancel());
            service.setPublicTrafficAllowed(false);
            QTRY_COMPARE(service.state(), QStringLiteral("unavailable"));
            QVERIFY(!service.canCheck()); QVERIFY(service.statusText().contains(QStringLiteral("paused")));
        }
    }
};

QTEST_GUILESS_MAIN(UpdateServiceTest)
#include "test_update.moc"
