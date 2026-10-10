#pragma once
// Scripted loopback HTTP backend for controller tests. The handler sees every
// complete request and returns a raw response; an empty response drops the
// connection the way an unreachable host would.
#include <QHostAddress>
#include <QObject>
#include <QTcpServer>
#include <QTcpSocket>
#include <functional>

inline QByteArray httpResponse(int status, const QByteArray &body, const QByteArray &extraHeaders = {}) {
    return "HTTP/1.1 " + QByteArray::number(status) + " Test\r\nContent-Type: application/json\r\nContent-Length: "
        + QByteArray::number(body.size()) + "\r\n" + extraHeaders + "Connection: close\r\n\r\n" + body;
}

class ScriptedBackend : public QObject {
public:
    std::function<QByteArray(const QByteArray &request)> respond;
    QList<QByteArray> requests;
    bool listen() {
        if (!m_server.listen(QHostAddress::LocalHost)) return false;
        connect(&m_server, &QTcpServer::newConnection, this, [this] {
            while (auto socket = m_server.nextPendingConnection()) {
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
                connect(socket, &QTcpSocket::readyRead, socket, [this, socket] {
                    if (socket->property("handled").toBool()) return;
                    const QByteArray request = socket->property("request").toByteArray() + socket->readAll();
                    socket->setProperty("request", request);
                    if (!request.isEmpty() && request[0] == char(0x16)) { socket->disconnectFromHost(); return; }
                    if (!request.contains("\r\n\r\n")) return;
                    socket->setProperty("handled", true);
                    requests.append(request);
                    const QByteArray response = respond ? respond(request) : QByteArray();
                    if (response.isEmpty()) { socket->abort(); socket->deleteLater(); return; }
                    socket->write(response);
                    socket->disconnectFromHost();
                });
            }
        });
        return true;
    }
    QString url() const { return QStringLiteral("http://127.0.0.1:%1").arg(m_server.serverPort()); }
    int count(const QByteArray &prefix) const {
        int total = 0;
        for (const auto &request : requests) if (request.startsWith(prefix)) ++total;
        return total;
    }
private:
    QTcpServer m_server;
};
