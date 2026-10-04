#include <QCoreApplication>
#include <QFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTcpServer>
#include <QTcpSocket>
#include <QSslServer>
#include <QTextStream>
#include <QTimer>
#include <cstdio>
#include <memory>
#include "http_assertions.h"
#include "tls_fixture.h"

int main(int argc, char **argv)
{
#ifdef Q_OS_MACOS
    if (qEnvironmentVariable("HEADROOM_TLS_FIXTURE_EMPTY_KEYCHAIN_PROBE") != "1")
        qputenv("QT_SSL_USE_TEMPORARY_KEYCHAIN", "1");
#endif
    QCoreApplication app(argc, argv);
    if (!TlsFixture::selectNativeTestBackend()) return 13;
    const QStringList arguments = app.arguments();
    const int listenIndex = arguments.indexOf(QStringLiteral("--listen-addr"));
    if (listenIndex < 0 || listenIndex + 1 >= arguments.size()) return 2;
    const QString address = arguments.at(listenIndex + 1);
    const bool desktopSession = arguments.contains(QStringLiteral("--desktop-session"));
    const int colon = address.lastIndexOf(':');
    if (!address.startsWith(QStringLiteral("127.0.0.1:")) || colon < 0) return 3;
    bool ok = false;
    const quint16 port = address.mid(colon + 1).toUShort(&ok);
    if (!ok || !port) return 4;

    const auto mode = qEnvironmentVariable("HEADROOM_FIXTURE_MODE", "degraded");
    QByteArray sessionNonce;
    QByteArray sessionToken;
    if (desktopSession) {
        QFile input;
        if (!input.open(stdin, QIODevice::ReadOnly)) return 9;
        const QByteArray frame = input.readAll();
        QJsonParseError error;
        const auto object = QJsonDocument::fromJson(frame.trimmed(), &error).object();
        sessionNonce = object.value(QStringLiteral("nonce")).toString().toLatin1();
        sessionToken = object.value(QStringLiteral("token")).toString().toLatin1();
        if (error.error != QJsonParseError::NoError || !object.value(QStringLiteral("schema")).isDouble()
            || object.value(QStringLiteral("schema")).toInt() != 1 || sessionNonce.size() != 32
            || sessionToken.size() != 64 || !frame.endsWith('\n')) return 10;
    }
    const auto recordPath = qEnvironmentVariable("HEADROOM_FIXTURE_RECORD");
    if (!recordPath.isEmpty()) {
        QFile record(recordPath);
        // Keep fixture assertions identical on Windows and Unix.
        if (record.open(QIODevice::WriteOnly | QIODevice::Append)) {
            QTextStream stream(&record);
            stream << "start\nargs=" << arguments.mid(1).join('|') << "\n"
                   << "token_present=" << (!qEnvironmentVariable("USAGE_AUTH_TOKEN").isEmpty() ? "yes" : "no") << "\n"
                   << "desktop_session=" << (desktopSession ? "yes" : "no") << "\n"
                   << "pid=" << QCoreApplication::applicationPid() << "\n";
        }
    }
    if (mode == QStringLiteral("crash")) {
        QTimer::singleShot(20, &app, [&app] { app.exit(7); });
        return app.exec();
    }

    std::unique_ptr<QTcpServer> server;
    if (desktopSession) {
        auto tlsServer = std::make_unique<QSslServer>();
        TlsFixture::configure(*tlsServer);
        server = std::move(tlsServer);
    } else {
        server = std::make_unique<QTcpServer>();
    }
    if (!server->listen(QHostAddress::LocalHost, port)) return 5;
    if (desktopSession && mode != QStringLiteral("identity-hang")) {
        QJsonObject identity{
            {QStringLiteral("schema"), 1},
            {QStringLiteral("nonce"), mode == QStringLiteral("bad-nonce") ? QString(32, '0') : QString::fromLatin1(sessionNonce)},
            {QStringLiteral("address"), QStringLiteral("127.0.0.1:%1").arg(port)},
            {QStringLiteral("certificate"), QString::fromLatin1(
                mode == QStringLiteral("wrong-cert") ? TlsFixture::replacementCertificatePem
                : TlsFixture::certificatePem) + (mode == QStringLiteral("trailing-cert") ? QStringLiteral("junk") : QString())},
        };
        QByteArray frame = QJsonDocument(identity).toJson(QJsonDocument::Compact) + '\n';
        if (mode == QStringLiteral("escaped-duplicate"))
            frame.replace("\"schema\":1", "\"schema\":1,\"sch\\u0065ma\":1");
        if (mode == QStringLiteral("extra-output")) frame += "{}\n";
        QFile output;
        if (!output.open(stdout, QIODevice::WriteOnly)) return 11;
        if (output.write(frame) != frame.size()) return 12;
        output.close();
    }
    QObject::connect(server.get(), &QTcpServer::pendingConnectionAvailable, &app, [&] {
        while (auto socket = server->nextPendingConnection()) {
            const auto readRequest = [socket, mode, sessionToken, desktopSession, recordPath] {
                QByteArray request = socket->property("request").toByteArray() + socket->readAll();
                socket->setProperty("request", request);
                if (!request.contains("\r\n\r\n") || socket->property("handled").toBool()) return;
                if (mode == QStringLiteral("hang")) return;
                socket->setProperty("handled", true);
                if (!recordPath.isEmpty()) {
                    QFile record(recordPath);
                    if (record.open(QIODevice::WriteOnly | QIODevice::Append)) {
                        record.write("http_request=yes\n");
                        record.write(request.first(request.indexOf("\r\n")) + '\n');
                    }
                }
                const bool authorized = !desktopSession || HttpAssertions::hasHeader(request, "Authorization", "Bearer " + sessionToken);
                QByteArray body;
                int status = 200;
                if (!authorized) {
                    status = 401; body = R"({"error":"unauthorized"})";
                } else if (request.startsWith("GET /api/v1/health ")) {
                    body = R"({"status":"degraded","version":"fixture-1","providers":[{"name":"fixture","ok":false,"fetched_at":null}]})";
                    if (mode == QStringLiteral("ready-crash"))
                        QTimer::singleShot(75, qApp, [] { QCoreApplication::exit(8); });
                } else if (mode == QStringLiteral("controller-recovery") && request.startsWith("GET /api/v1/usage ")) {
                    body = R"([{"provider_name":"Claude","subtitle":null,"error":null,"needs_reauth":false,"is_success":true,"buckets":[{"id":"session","label":"Current","utilization":12,"resets_at":null,"status_text":null}]},{"provider_name":"Cursor","subtitle":null,"error":"Cursor session expired. Log in to cursor.com again.","needs_reauth":true,"is_success":false,"buckets":[]}])";
                } else if (mode == QStringLiteral("controller-recovery") && request.startsWith("PUT /api/v1/providers/cursor/credentials ")) {
                    body = R"({"provider":"Cursor","refetched":true,"usage":{"provider_name":"Cursor","subtitle":null,"error":null,"needs_reauth":false,"is_success":true,"buckets":[{"id":"weekly","label":"Weekly","utilization":22,"resets_at":null,"status_text":null}]}})";
                } else if (request.startsWith("GET /api/v1/usage ")) {
                    if (mode == QStringLiteral("ready-crash")) return;
                    body = QByteArrayLiteral("[]");
                } else {
                    status = 404; body = R"({"error":"not found"})";
                }
                socket->write("HTTP/1.1 " + QByteArray::number(status) + " Fixture\r\nContent-Type: application/json\r\nContent-Length: "
                    + QByteArray::number(body.size()) + "\r\nConnection: close\r\n\r\n" + body);
                socket->disconnectFromHost();
            };
            QObject::connect(socket, &QTcpSocket::readyRead, socket, readRequest);
            QMetaObject::invokeMethod(socket, readRequest, Qt::QueuedConnection);
            QObject::connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
        }
    });
    return app.exec();
}
