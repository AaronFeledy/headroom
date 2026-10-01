#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QTextStream>
#include <QTimer>

int main(int argc, char **argv)
{
    QCoreApplication app(argc, argv);
    const QStringList arguments = app.arguments();
    if (arguments.size() != 2 || (arguments[1] != QStringLiteral("cursor") && arguments[1] != QStringLiteral("grok"))) return 2;
    const QString mode = qEnvironmentVariable("HEADROOM_CREDENTIAL_FIXTURE_MODE", QStringLiteral("valid"));
    const QString recordPath = qEnvironmentVariable("HEADROOM_CREDENTIAL_FIXTURE_RECORD");
    const QString snapshotRoot = qEnvironmentVariable("HEADROOM_CREDENTIAL_SNAPSHOT_ROOT");
    if (!recordPath.isEmpty()) {
        QFile record(recordPath);
        if (record.open(QIODevice::WriteOnly | QIODevice::Append)) {
            record.write("start\n");
            if (mode == QStringLiteral("hang")) record.write(snapshotRoot.toUtf8() + "\n");
        }
    }
    if (mode == QStringLiteral("hang")) {
        QDir().mkpath(snapshotRoot);
        QFile artifact(QDir(snapshotRoot).filePath(QStringLiteral("interrupted-snapshot")));
        if (artifact.open(QIODevice::WriteOnly)) artifact.write("synthetic");
        QTimer::singleShot(60000, &app, &QCoreApplication::quit);
        return app.exec();
    }
    if (mode == QStringLiteral("oversized")) {
        QTextStream(stdout) << QString(70 * 1024, QLatin1Char('x'));
        return 0;
    }
    if (mode == QStringLiteral("malformed")) {
        QTextStream(stdout) << "not-json";
        return 0;
    }
    const QString cookie = arguments[1] == QStringLiteral("cursor")
        ? QStringLiteral("WorkosCursorSessionToken=synthetic-cursor")
        : QStringLiteral("sso=synthetic-grok");
    QJsonObject output{{QStringLiteral("provider"), arguments[1]}, {QStringLiteral("cookie"), cookie}};
    if (mode == QStringLiteral("sources")) {
        output[QStringLiteral("source")] = QStringLiteral("Firefox");
        output[QStringLiteral("checked")] = QJsonArray{QJsonObject{{"name", "Chrome"}, {"status", "encrypted"}},
            QJsonObject{{"name", "Firefox"}, {"status", "signed_in"}}};
    }
    QTextStream(stdout) << QJsonDocument(output).toJson(QJsonDocument::Compact);
    return 0;
}
