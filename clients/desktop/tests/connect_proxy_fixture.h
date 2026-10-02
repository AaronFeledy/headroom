#pragma once
#include <QNetworkProxy>
#include <QTcpServer>
#include <QTcpSocket>

// Resolve only the synthetic remote fixture through a loopback CONNECT tunnel.
// This avoids relying on nonportable 127/8 aliases or external DNS changes.
class ConnectProxyFixture : public QTcpServer {
public:
    QList<QByteArray> requests;
    explicit ConnectProxyFixture(quint16 targetPort) {
        connect(this, &QTcpServer::newConnection, this, [this, targetPort] {
            while (hasPendingConnections()) {
                auto socket = nextPendingConnection();
                connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
                connect(socket, &QTcpSocket::readyRead, socket, [this, socket, targetPort] {
                    if (socket->property("tunneling").toBool()) return;
                    auto bytes = socket->property("request").toByteArray() + socket->readAll();
                    socket->setProperty("request", bytes);
                    if (!bytes.contains("\r\n\r\n")) return;
                    socket->setProperty("tunneling", true); requests.append(bytes);
                    if (!bytes.startsWith("CONNECT headroom-proxy-fixture.invalid:")) {
                        socket->write("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n");
                        socket->disconnectFromHost(); return;
                    }
                    auto upstream = new QTcpSocket(socket);
                    upstream->setProxy(QNetworkProxy::NoProxy);
                    connect(upstream, &QTcpSocket::connected, socket, [socket] {
                        socket->write("HTTP/1.1 200 Connection Established\r\n\r\n");
                    });
                    connect(upstream, &QTcpSocket::readyRead, socket, [socket, upstream] { socket->write(upstream->readAll()); });
                    connect(socket, &QTcpSocket::readyRead, upstream, [socket, upstream] { upstream->write(socket->readAll()); });
                    connect(upstream, &QTcpSocket::disconnected, socket, &QTcpSocket::disconnectFromHost);
                    upstream->connectToHost(QHostAddress::LocalHost, targetPort);
                });
            }
        });
    }
    QNetworkProxy proxy() const {
        return QNetworkProxy(QNetworkProxy::HttpProxy, "127.0.0.1", serverPort());
    }
};
