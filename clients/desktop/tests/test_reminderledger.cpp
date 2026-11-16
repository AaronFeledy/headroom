#include "reminderledger.h"
#include <QFile>
#include <QFileInfo>
#include <QTemporaryDir>
#include <QtTest>
#include "statemachine_assertions.h"

class ReminderLedgerTest : public QObject {
    Q_OBJECT
private slots:
    void init() {
        failOnStateMachineWarning();
    }
    void failedSaveIsRetried() {
        QTemporaryDir dir; QVERIFY(dir.isValid());
        // A file where the ledger's directory belongs makes every write fail.
        QFile blocker(dir.filePath("state"));
        QVERIFY(blocker.open(QIODevice::WriteOnly)); blocker.close();
        const QString path = dir.filePath("state/settings.reminders.json");
        ReminderLedger ledger(path);
        ledger.add({QString(32, 'a'), "expiry", "", 1800000000000, 3});
        ledger.flush();
        const QString failure = ledger.error();
        QVERIFY(!failure.isEmpty());
        // A persistent failure retries with the same error, which the controller logs once.
        ledger.flush();
        QCOMPARE(ledger.error(), failure);
        QVERIFY(!QFileInfo::exists(path));
        QVERIFY(QFile::remove(blocker.fileName()));
        // Nothing changed since the failure, but the record was never saved.
        ledger.flush();
        QVERIFY(ledger.error().isEmpty());
        QCOMPARE(ReminderLedger(path).records().size(), 1);
    }
};
QTEST_MAIN(ReminderLedgerTest)
#include "test_reminderledger.moc"
