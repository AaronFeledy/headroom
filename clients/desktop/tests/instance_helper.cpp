#include "instance.h"

#include <QCoreApplication>
#include <QFile>
#include <QLocalServer>
#include <QLocalSocket>
#include <QLockFile>
#include <QTimer>

namespace {
bool writeMarker(const QString &path, const QByteArray &contents)
{
    QFile file(path);
    return file.open(QIODevice::WriteOnly) && file.write(contents) == contents.size();
}
}

int main(int argc, char **argv)
{
    QCoreApplication app(argc, argv);
    app.setOrganizationName("HeadroomTests");
    app.setApplicationName("InstanceFixture");
    const auto arguments = app.arguments();
    if (arguments.size() < 4 || arguments.size() > 5) return 2;
    InstanceService instance(arguments[1]);
    if (arguments.size() == 5 && arguments[4] == "lock-only") {
        QLockFile lock(instance.lockPath());
        lock.setStaleLockTime(0);
        if (!lock.tryLock(0)) return 7;
        if (!writeMarker(arguments[2], "ready")) return 4;
        QTimer::singleShot(10000, &app, [&] { app.exit(6); });
        return app.exec();
    }
    if (arguments.size() == 5 && arguments[4] == "noack") {
        QLockFile lock(instance.lockPath());
        lock.setStaleLockTime(0);
        if (!lock.tryLock()) return 7;
        QLocalServer::removeServer(instance.scopeName());
        QLocalServer server;
#ifdef Q_OS_WIN
        server.setSocketOptions(QLocalServer::UserAccessOption);
#endif
        if (!server.listen(instance.scopeName())) return 8;
        QObject::connect(&server, &QLocalServer::newConnection, &app, [&] {
            auto socket = server.nextPendingConnection();
            QObject::connect(socket, &QLocalSocket::readyRead, socket, [socket] { socket->readAll(); });
            QObject::connect(socket, &QLocalSocket::disconnected, &app, &QCoreApplication::quit);
        });
        if (!writeMarker(arguments[2], "ready")) return 4;
        QTimer::singleShot(10000, &app, [&] { app.exit(6); });
        return app.exec();
    }
    if (instance.start(2000) != InstanceService::Result::Primary) return 3;
    instance.setRequestHandler([](const QByteArray &request) {
        return request == R"({"command":"usage"})" ? QByteArray(R"({"ok":true,"result":[]})") : QByteArray();
    });
    QObject::connect(&instance, &InstanceService::activationRequested, &app, [&] {
        if (!writeMarker(arguments[3], "activated")) app.exit(5);
    });
    if (!writeMarker(arguments[2], "ready")) return 4;
    QTimer::singleShot(10000, &app, [&] { app.exit(6); });
    return app.exec();
}
