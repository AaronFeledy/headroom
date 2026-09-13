#include "remoteupdate.h"
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSignalSpy>
#include <QTemporaryDir>
#include <QTest>

class RemoteUpdateTest final : public QObject {
    Q_OBJECT
    QTemporaryDir m_dir;
    QStringList m_logs;
    QString tracePath() const { return m_dir.filePath("trace.jsonl"); }
    RemoteUpdateOptions options() {
        RemoteUpdateOptions result;
        result.ssh = {QStringLiteral(REMOTE_UPDATE_FIXTURE_PATH), 500};
        result.commandTimeoutMs = 1500;
        result.verificationTimeoutMs = 400;
        result.pollIntervalMs = 10;
        result.diagnostic = [this](const QString &line) { m_logs << line; };
        return result;
    }
    QList<QJsonObject> trace() const {
        QFile file(tracePath());
        if (!file.open(QIODevice::ReadOnly)) return {};
        QList<QJsonObject> result;
        for (const auto &line : file.readAll().split('\n'))
            if (!line.isEmpty()) result << QJsonDocument::fromJson(line).object();
        return result;
    }
    int updateCount() const {
        int count = 0;
        for (const auto &record : trace()) if (record["update"].toBool()) ++count;
        return count;
    }
private slots:
    void init() {
        QVERIFY(m_dir.isValid()); QFile::remove(tracePath()); m_logs.clear();
        qputenv("HEADROOM_REMOTE_UPDATE_TRACE", tracePath().toUtf8());
    }
    void cleanup() { qunsetenv("HEADROOM_REMOTE_UPDATE_TRACE"); }

    void explicitSshActionOnly() {
        RemoteUpdateService service(options());
        service.setEnabled(true);
        for (const QString &address : {QString(), QString("http://127.0.0.1:7823"), QString("https://remote.test"), QString("ssh://user:secret@remote.test"), QString("ssh://remote.test/path")}) {
            service.setBackend(address); service.start();
            QVERIFY(!service.available()); QVERIFY(!service.busy());
        }
        service.setBackend("ssh://user@current:2222");
        QVERIFY(service.canStart());
        QTest::qWait(20); QVERIFY(trace().isEmpty());
        service.setEnabled(false); service.start();
        QVERIFY(!service.canStart()); QVERIFY(!service.busy()); QVERIFY(trace().isEmpty());
    }

    void verifiesServerAndNeverRepeatsUpdate_data() {
        QTest::addColumn<QString>("host");
        for (const auto *host : {"current", "staged", "desktop", "restarting", "degraded"})
            QTest::newRow(host) << QString::fromLatin1(host);
    }
    void verifiesServerAndNeverRepeatsUpdate() {
        QFETCH(QString, host);
        RemoteUpdateService service(options());
        service.setEnabled(true); service.setBackend("ssh://alice@" + host + ":2222");
        QSignalSpy complete(&service, &RemoteUpdateService::completed);
        service.start(); service.start(); service.start();
        QVERIFY(service.busy()); QVERIFY(!service.canStart());
        QTRY_COMPARE(service.state(), QString("current"));
        QCOMPARE(complete.size(), 1); QCOMPARE(updateCount(), 1);
        QVERIFY(service.statusText().contains("2.1.0"));
        QVERIFY(m_logs.join('\n').contains("remote-update:"));
        QVERIFY(m_logs.join('\n').contains("Waiting for the server"));
        const auto records = trace(); QVERIFY(records.size() >= 2);
        const auto args = records.first()["arguments"].toArray().toVariantList();
        QCOMPARE(args.last().toString(), QString("headroom update --this-install-only"));
        for (const auto *flag : {"BatchMode=yes", "StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes", "ConnectionAttempts=1", "ControlPath=none"})
            QVERIFY(args.contains(QString::fromLatin1(flag)));
        if (host == "restarting") QVERIFY(records.size() >= 3);
        for (int index = 1; index < records.size(); ++index)
            QCOMPARE(records[index]["arguments"].toArray().last().toString(), QString("usage-server --ssh-stdio"));
    }

    void rejectsFailuresAndUnknownOutcomes_data() {
        QTest::addColumn<QString>("host"); QTest::addColumn<QString>("message");
        QTest::newRow("not-found") << "missing" << "not found";
        QTest::newRow("ssh") << "ssh-failure" << "SSH session failed";
        QTest::newRow("cli") << "failure" << "did not complete";
        QTest::newRow("restricted-key") << "restricted" << "did not confirm";
        QTest::newRow("invalid-version") << "invalid-version" << "did not confirm";
        QTest::newRow("oversized") << "large" << "output";
        QTest::newRow("old-server") << "mismatch" << "expected version";
        QTest::newRow("invalid-health") << "bad-health" << "expected version";
        QTest::newRow("hung-health") << "slow-health" << "expected version";
    }
    void rejectsFailuresAndUnknownOutcomes() {
        QFETCH(QString, host); QFETCH(QString, message);
        RemoteUpdateService service(options());
        service.setEnabled(true); service.setBackend("ssh://" + host);
        QSignalSpy complete(&service, &RemoteUpdateService::completed);
        service.start(); QTRY_COMPARE(service.state(), QString("failed"));
        QVERIFY2(service.statusText().contains(message), qPrintable(service.statusText()));
        QCOMPARE(complete.size(), 0); QCOMPARE(updateCount(), 1);
        QTest::qWait(30); QCOMPARE(updateCount(), 1);
        const auto visible = service.statusText() + m_logs.join('\n');
        QVERIFY(!visible.contains("private-fixture-token"));
        QVERIFY(!visible.contains("user:secret"));
        QVERIFY(!visible.contains("fixture.invalid"));
    }

    void commandTimeoutAndMissingSsh() {
        auto config = options(); config.commandTimeoutMs = 100;
        RemoteUpdateService service(config);
        service.setEnabled(true); service.setBackend("ssh://timeout"); service.start();
        QTRY_COMPARE(service.state(), QString("failed"));
        QVERIFY(service.statusText().contains("outcome is unknown"));
        QVERIFY(updateCount() <= 1);
        config.ssh.executablePath = m_dir.filePath("not-installed");
        RemoteUpdateService missing(config);
        missing.setEnabled(true); missing.setBackend("ssh://current"); missing.start();
        QTRY_COMPARE(missing.state(), QString("failed"));
        QVERIFY(missing.statusText().contains("could not be started"));
    }

    void connectionChangeStopsVerificationWithoutUpdatingNewHost() {
        auto config = options(); config.verificationTimeoutMs = 2000;
        RemoteUpdateService service(config);
        service.setEnabled(true); service.setBackend("ssh://mismatch"); service.start();
        QTRY_COMPARE(service.state(), QString("verifying"));
        service.setBackend("ssh://current");
        QCOMPARE(service.state(), QString("failed")); QVERIFY(!service.busy());
        QVERIFY(service.statusText().contains("previous server"));
        QTest::qWait(100); QCOMPARE(updateCount(), 1);
        for (const auto &record : trace()) QVERIFY(!record["arguments"].toArray().contains("current"));
    }

    void stripsCredentialEnvironmentFromUpdateCommand() {
        const auto previousUsage = qgetenv("USAGE_AUTH_TOKEN"), previousHeadroom = qgetenv("HEADROOM_AUTH_TOKEN");
        const bool hadUsage = qEnvironmentVariableIsSet("USAGE_AUTH_TOKEN"), hadHeadroom = qEnvironmentVariableIsSet("HEADROOM_AUTH_TOKEN");
        qputenv("USAGE_AUTH_TOKEN", "private-fixture-token"); qputenv("HEADROOM_AUTH_TOKEN", "private-fixture-token");
        RemoteUpdateService service(options());
        service.setEnabled(true); service.setBackend("ssh://current"); service.start();
        if (hadUsage) qputenv("USAGE_AUTH_TOKEN", previousUsage); else qunsetenv("USAGE_AUTH_TOKEN");
        if (hadHeadroom) qputenv("HEADROOM_AUTH_TOKEN", previousHeadroom); else qunsetenv("HEADROOM_AUTH_TOKEN");
        QTRY_COMPARE(service.state(), QString("current"));
        QVERIFY(!trace().first()["has_auth_environment"].toBool());
    }
};

QTEST_GUILESS_MAIN(RemoteUpdateTest)
#include "test_remoteupdate.moc"
