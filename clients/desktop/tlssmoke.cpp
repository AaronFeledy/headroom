#include "tlssmoke.h"
#include "controller.h"
#include <QCoreApplication>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSaveFile>
#include <QSslSocket>
#include <QTimer>
#include <QUrl>

void startPackagedTlsSmoke(Controller &controller, const QString &resultPath) {
    auto *timer = new QTimer(&controller);
    timer->setInterval(100);
    QObject::connect(timer, &QTimer::timeout, &controller, [&controller, resultPath, timer] {
        if (controller.state().value("status").toString() != "ready") return;
        const auto server = controller.ownedServerExecutablePath();
        const auto expected = QCoreApplication::applicationDirPath() + "/usage-server"
#ifdef Q_OS_WIN
            ".exe"
#endif
            ;
        if (controller.settings().value("mode").toString() != "local"
            || controller.ownedServerProcessId() <= 0 || controller.backendCertificate().isNull()
            || QUrl(controller.backendUrl()).scheme() != "https"
            || QFileInfo(server).canonicalFilePath() != QFileInfo(expected).canonicalFilePath()) return;
        const QJsonObject result{{"ok", true}, {"backend", QSslSocket::activeBackend()},
            {"qt_version", qVersion()}, {"version", QCoreApplication::applicationVersion()}};
        QSaveFile output(resultPath);
        const auto bytes = QJsonDocument(result).toJson(QJsonDocument::Compact);
        const bool written = output.open(QIODevice::WriteOnly) && output.write(bytes) == bytes.size() && output.commit();
        timer->stop();
        QCoreApplication::exit(written ? 0 : 1);
    });
    timer->start();
    QTimer::singleShot(25000, &controller, [] { QCoreApplication::exit(1); });
}
