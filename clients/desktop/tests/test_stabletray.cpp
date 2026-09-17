#include "stabletray.h"
#include <QApplication>
#include <QFile>
#include <QJsonDocument>
#include <QSignalSpy>
#include <QtTest>
#include <cstdio>

// A private pipe peer, never a Shell icon or a live provider connection.
static int fixture() {
    const auto mode = qgetenv("HEADROOM_TRAY_FIXTURE");
    char line[32770];
    bool ready = false;
    const auto emitEvent = [](QJsonObject event) {
        event.insert("version", 1);
        const auto data = QJsonDocument(event).toJson(QJsonDocument::Compact) + '\n';
        fwrite(data.constData(), 1, size_t(data.size()), stdout); fflush(stdout);
    };
    while (fgets(line, sizeof(line), stdin)) {
        const auto cmd = QJsonDocument::fromJson(line).object();
        if (cmd.value("op") == "icon" && !ready) {
            const int size = cmd.value("size").toInt();
            if (QByteArray::fromBase64(cmd.value("pixels").toString().toLatin1()).size() != size*size*4) return 2;
            ready = true;
            if (mode == "bad") { fputs("invalid\n", stdout); fflush(stdout); continue; }
            emitEvent({{"event", "ready"}, {"size", 24}});
            emitEvent({{"event", "activate"}, {"reason", 3}});
            if (mode == "exit") return 0;
        }
        if (cmd.value("op") == "notify") emitEvent({{"event", "message"}});
        if (cmd.value("op") == "quit") return 0;
    }
    return 0;
}

class StableTrayTest : public QObject {
    Q_OBJECT
private:
    QIcon icon() { QPixmap pixmap(32,32); pixmap.fill(Qt::magenta); return QIcon(pixmap); }
private slots:
    void cleanup() { qunsetenv("HEADROOM_TRAY_FIXTURE"); }
    void shellIdentityMatchesLauncher() {
        QCOMPARE(StableTray::shellIdentity(R"(C:\Users\Test\Headroom\headroom.exe)").toHex(),
                 QByteArray("7974f8089c125c1c98618b11a2f2235f"));
        QCOMPARE(StableTray::shellIdentity("c:/users/test/headroom/headroom.exe"),
                 StableTray::shellIdentity(R"(C:\Users\Test\Headroom\headroom.exe)"));
        QVERIFY(StableTray::shellIdentity(R"(C:\Users\Other\Headroom\headroom.exe)")
                != StableTray::shellIdentity(R"(C:\Users\Test\Headroom\headroom.exe)"));
    }
    void pipeLifecycle() {
        qputenv("HEADROOM_TRAY_FIXTURE", "normal");
        StableTray tray;
        QSignalSpy availability(&tray, &StableTray::availabilityChanged);
        QSignalSpy activation(&tray, &StableTray::activated);
        QSignalSpy message(&tray, &StableTray::messageClicked);
        tray.setIcon(icon()); tray.setToolTip("Synthetic usage");
        tray.start(QCoreApplication::applicationFilePath());
        QTRY_VERIFY(tray.available());
        QTRY_COMPARE(activation.size(), 1);
        QCOMPARE(activation[0][0].toInt(), 3);
        tray.showMessage("Synthetic", "Test notice", 2);
        QTRY_COMPARE(message.size(), 1);
        tray.stop();
        QVERIFY(!tray.available());
        QCOMPARE(availability.size(), 1);
    }
    void helperExitRestoresFallback() {
        qputenv("HEADROOM_TRAY_FIXTURE", "exit");
        StableTray tray;
        QSignalSpy availability(&tray, &StableTray::availabilityChanged);
        tray.setIcon(icon()); tray.start(QCoreApplication::applicationFilePath());
        QTRY_COMPARE(availability.size(), 2);
        QVERIFY(availability[0][0].toBool());
        QVERIFY(!availability[1][0].toBool());
        QVERIFY(!tray.available());
    }
    void malformedPeerNeverReplacesFallback() {
        qputenv("HEADROOM_TRAY_FIXTURE", "bad");
        StableTray tray;
        QSignalSpy availability(&tray, &StableTray::availabilityChanged);
        tray.setIcon(icon()); tray.start(QCoreApplication::applicationFilePath());
        QTest::qWait(500);
        QVERIFY(!tray.available());
        QCOMPARE(availability.size(), 0);
        tray.stop();
    }
};

int main(int argc, char **argv) {
    if (argc == 2 && QByteArray(argv[1]) == "--headroom-tray-host") return fixture();
    QApplication app(argc, argv);
    StableTrayTest test;
    return QTest::qExec(&test, argc, argv);
}
#include "test_stabletray.moc"
