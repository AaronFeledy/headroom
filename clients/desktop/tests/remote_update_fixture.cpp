#include <QCoreApplication>
#include <QFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QThread>
#include <cstdio>

// A process-only fixture: it never starts ssh, a real updater, or any provider.
int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    const auto args = app.arguments();
    const int separator = args.indexOf("--");
    if (separator < 0 || separator + 3 != args.size()) return 9;
    const QString host = args[separator + 1];
    const bool updating = args.last() == QStringLiteral("headroom update --this-install-only");
    if (!updating && args.last() != QStringLiteral("usage-server --ssh-stdio")) return 9;
    QFile input; if (!input.open(stdin, QIODevice::ReadOnly)) return 9;
    const QByteArray body = input.readAll();
    const auto request = QJsonDocument::fromJson(body).object();
    if (updating ? !body.isEmpty() : request.value("method") != "GET" || request.value("path") != "/api/v1/health" || request.value("body") != "") return 9;
    QFile trace(qEnvironmentVariable("HEADROOM_REMOTE_UPDATE_TRACE"));
    if (!trace.open(QIODevice::ReadWrite | QIODevice::Append)) return 9;
    trace.seek(0);
    const auto previous = trace.readAll();
    const QJsonObject record{{"arguments", QJsonArray::fromStringList(args.mid(1))}, {"update", updating},
        {"has_auth_environment", !qEnvironmentVariableIsEmpty("USAGE_AUTH_TOKEN") || !qEnvironmentVariableIsEmpty("HEADROOM_AUTH_TOKEN")}};
    trace.write(QJsonDocument(record).toJson(QJsonDocument::Compact) + '\n'); trace.close();
    if (updating) {
        if (host == "timeout") QThread::msleep(2000);
        if (host == "large") { const QByteArray bytes(65537, 'x'); fwrite(bytes.constData(), 1, size_t(bytes.size()), stdout); return 0; }
        if (host == "failure" || host == "missing" || host == "ssh-failure") {
            fputs("private-fixture-token https://user:secret@fixture.invalid/token\n", stderr);
            return host == "missing" ? 127 : host == "ssh-failure" ? 255 : 1;
        }
        if (host == "restricted") { fputs("{\"schema\":1,\"status\":400,\"body\":\"e30=\"}\n", stdout); return 0; }
        if (host == "invalid-version") { fputs("Headroom private-fixture-token is current.\n", stdout); return 0; }
        if (host == "current") fputs("Headroom 2.1.0 is current.\n", stdout);
        else if (host == "desktop") fputs("Headroom update accepted (2.1.0).\n", stdout);
        else fputs("Headroom 2.1.0 is staged and will finish installing now.\n", stdout);
        return 0;
    }
    if (host == "slow-health") QThread::msleep(2000);
    const bool restarting = host == "restarting" && !previous.contains("\"update\":false");
    const QJsonObject health{{"status", host == "bad-health" ? "invalid" : host == "degraded" ? "degraded" : "ok"},
        {"version", host == "mismatch" ? "2.0.0" : "2.1.0"}};
    const QJsonObject response{{"schema", 1}, {"status", restarting ? 503 : 200},
        {"body", QString::fromLatin1(QJsonDocument(health).toJson(QJsonDocument::Compact).toBase64())}};
    const auto output = QJsonDocument(response).toJson(QJsonDocument::Compact) + '\n';
    fwrite(output.constData(), 1, size_t(output.size()), stdout);
    return 0;
}
