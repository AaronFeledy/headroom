#include <QCoreApplication>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTextStream>
#include <QSysInfo>
#include <QDir>

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    const QStringList args = app.arguments();
    const QString command = args.size() > 1 ? args.at(1) : QString();
    const QString mode = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_MODE", "available");
    if (const QString record = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_RECORD"); !record.isEmpty()) {
        QFile file(record);
        if (file.open(QIODevice::Append)) {
            QTextStream stream(&file);
            stream << command << " token=" << (qEnvironmentVariableIsSet("USAGE_AUTH_TOKEN") ? "present" : "absent")
                   << " args=" << args.mid(2).join('|') << '\n';
        }
    }
    if (command == QStringLiteral("update")) {
        QFile input;
        if (input.open(stdin, QIODevice::ReadOnly)) input.read(1);
        return 0;
    }
    if (command == QStringLiteral("prepare-apply")) {
		if (mode == QStringLiteral("hang")) { QFile input; if (input.open(stdin, QIODevice::ReadOnly)) input.read(1); return 2; }
		if (mode == QStringLiteral("malformed")) { QTextStream(stdout) << "not json\n"; return 0; }
        const auto valueAfter = [&](const QString &flag) {
            const int index = args.indexOf(flag);
            return index >= 0 && index + 1 < args.size() ? args.at(index + 1) : QString();
        };
        const QString installRoot = valueAfter(QStringLiteral("--install-root"));
        const QString directory = QDir(installRoot).filePath(QStringLiteral("transactions/apply-0123456789abcdef0123456789abcdef"));
        QDir().mkpath(directory);
#ifdef Q_OS_WIN
        const QString manager = QDir(directory).filePath(QStringLiteral("headroom-apply.exe"));
#else
        const QString manager = QDir(directory).filePath(QStringLiteral("headroom-apply"));
#endif
		QFile::remove(manager); if (!QFile::copy(QCoreApplication::applicationFilePath(), manager)) return 3;
        const QString requestPath = QDir(directory).filePath(QStringLiteral("apply-request.json"));
        const QString acknowledgement = QDir(directory).filePath(QStringLiteral("accepted.json"));
        const QString commit = QDir(directory).filePath(QStringLiteral("commit.json"));
        const QString nonce(48, QLatin1Char('a'));
        QJsonObject request{{"schema", 1}, {"product", "Headroom"}, {"install_root", installRoot},
            {"entry_path", valueAfter(QStringLiteral("--entry-path"))}, {"stage_record", valueAfter(QStringLiteral("--stage-record"))},
            {"current_pid", valueAfter(QStringLiteral("--current-pid")).toLongLong()},
            {"current_executable", valueAfter(QStringLiteral("--current-executable"))}, {"acknowledgement_path", acknowledgement},
            {"commit_path", commit}, {"nonce", nonce}};
        QJsonArray participants;
        for (qsizetype index = 1; index + 1 < args.size(); ++index) {
            if (args.at(index) != QStringLiteral("--participant")) continue;
            auto participant = QJsonDocument::fromJson(args.at(++index).toUtf8()).object();
            participant["process_token"] = "fixture-process-token";
            if (mode == QStringLiteral("mismatched-participant")) participant["pid"] = 1;
            participants.append(participant);
        }
        if (mode == QStringLiteral("managed-server")) {
            const QString cli = QDir(QFileInfo(valueAfter(QStringLiteral("--current-executable"))).absolutePath()).filePath(
#ifdef Q_OS_WIN
                QStringLiteral("headroom-cli.exe"));
#else
                QStringLiteral("headroom-cli"));
#endif
            participants.append(QJsonObject{{"role", "managed_server"}, {"pid", 23456},
                {"executable", cli}, {"process_token", "fixture-service-token"}});
        }
        if (!participants.isEmpty()) request["additional_processes"] = participants;
		QFile requestFile(requestPath);
		if (!requestFile.open(QIODevice::WriteOnly) || requestFile.write(QJsonDocument(request).toJson()) < 0) return 3;
		requestFile.close();
		QFile accepted(acknowledgement);
		if (!accepted.open(QIODevice::WriteOnly)
		    || accepted.write(QJsonDocument(QJsonObject{{"schema", 1}, {"product", "Headroom"}, {"accepted", true}, {"nonce", nonce}}).toJson()) < 0) return 3;
		accepted.close();
        const QString resultNonce = mode == QStringLiteral("mismatched-prepare") ? QString(48, QLatin1Char('b')) : nonce;
        const QJsonObject result{{"schema", 1}, {"product", "Headroom"}, {"transaction_directory", QDir::toNativeSeparators(directory)},
            {"request_path", QDir::toNativeSeparators(requestPath)}, {"manager_path", QDir::toNativeSeparators(manager)},
            {"acknowledgement_path", QDir::toNativeSeparators(acknowledgement)}, {"commit_path", QDir::toNativeSeparators(commit)}, {"nonce", resultNonce}};
        QTextStream(stdout) << QJsonDocument(QJsonObject{{"ok", true}, {"command", command}, {"result", result}}).toJson(QJsonDocument::Compact) << '\n';
        return 0;
    }
    if (command == QStringLiteral("inspect")) {
        QString architecture = QSysInfo::currentCpuArchitecture();
        if (architecture == QStringLiteral("amd64")) architecture = QStringLiteral("x86_64");
        if (architecture == QStringLiteral("aarch64")) architecture = QStringLiteral("arm64");
#ifdef Q_OS_WIN
        const QString platform = QStringLiteral("windows");
        const QString asset = QStringLiteral("Headroom-v0.1.0-windows-") + (architecture == QStringLiteral("x86_64") ? QStringLiteral("x64.zip") : QStringLiteral("arm64.zip"));
#elif defined(Q_OS_MACOS)
        const QString platform = QStringLiteral("macos");
        const QString asset = QStringLiteral("Headroom-v0.1.0-macos-") + architecture + QStringLiteral(".tar.gz");
#else
        const QString platform = QStringLiteral("linux");
        const QString asset = QStringLiteral("Headroom-v0.1.0-linux-x86_64.tar.gz");
#endif
        QJsonObject result{{"installed", true}, {"trusted_identity", true}, {"complete", !qEnvironmentVariableIsSet("HEADROOM_UPDATE_FIXTURE_MISSING")},
            {"version", "0.1.0"}, {"version_path", "versions/0.1.0"}, {"platform", platform}, {"architecture", architecture}, {"package_asset", asset},
            {"launcher_path", qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_INTERNAL_LAUNCHER", "/fixture/headroom-launcher")}};
        if (qEnvironmentVariableIsSet("HEADROOM_UPDATE_FIXTURE_CLI"))
            result["cli_entry_path"] = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_CLI");
		if (qEnvironmentVariableIsSet("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS")) {
			result["apply_status"] = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_APPLY_STATUS");
			result["apply_message"] = QStringLiteral("synthetic readiness failure");
		}
        if (qEnvironmentVariableIsSet("HEADROOM_UPDATE_FIXTURE_MISSING")) {
            const QString missing = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_MISSING") == QStringLiteral("1")
                ? QStringLiteral("bin/usage-server") : qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_MISSING");
            result["missing"] = QJsonArray{missing};
        }
        QTextStream(stdout) << QJsonDocument(QJsonObject{{"ok", true}, {"command", command}, {"result", result}}).toJson(QJsonDocument::Compact) << '\n';
        return 0;
    }
    if (mode == QStringLiteral("error")) {
        const QString error = qEnvironmentVariable("HEADROOM_UPDATE_FIXTURE_ERROR");
        QTextStream(stdout) << QJsonDocument(QJsonObject{{"ok", false}, {"command", command}, {"error", error}}).toJson(QJsonDocument::Compact) << '\n';
        QTextStream(stderr) << "private-stderr-value https://example.test/?token=private-token\n";
        return 2;
    }
    if (mode == QStringLiteral("hang")) {
        QFile input; if (input.open(stdin, QIODevice::ReadOnly)) input.read(1);
        QTextStream(stdout) << "{\"ok\":false,\"command\":\"" << command << "\",\"error\":\"cancelled\"}\n";
        return 2;
    }
    if (mode == QStringLiteral("stderr")) { QFile error; if (error.open(stderr, QIODevice::WriteOnly)) error.write(QByteArray(300 * 1024, 'x')); return 2; }
    if (mode == QStringLiteral("malformed")) { QTextStream(stdout) << "not json\n"; return 0; }
    QString status = command == QStringLiteral("check-update") ? mode : QStringLiteral("staged");
    if (command == QStringLiteral("check-update") && (mode == QStringLiteral("mismatched-participant") || mode == QStringLiteral("managed-server"))) status = QStringLiteral("available");
    QString version = command == QStringLiteral("stage-repair") ? QStringLiteral("0.1.0") : QStringLiteral("9.1.0");
    QString architecture = QSysInfo::currentCpuArchitecture();
    if (architecture == QStringLiteral("amd64")) architecture = QStringLiteral("x86_64");
    if (architecture == QStringLiteral("aarch64")) architecture = QStringLiteral("arm64");
#ifdef Q_OS_WIN
    const QString platform = QStringLiteral("windows");
#elif defined(Q_OS_MACOS)
    const QString platform = QStringLiteral("macos");
#else
    const QString platform = QStringLiteral("linux");
#endif
    QJsonObject result{{"status", status}, {"current_version", "0.1.0"}, {"version", version},
        {"platform", platform}, {"architecture", architecture}, {"asset_name", "fixture-package"}};
    if (status == QStringLiteral("staged")) {
        const int rootIndex = args.indexOf(QStringLiteral("--install-root"));
        const QString installRoot = rootIndex >= 0 && rootIndex + 1 < args.size() ? args.at(rootIndex + 1) : QString();
        const QString stageDirectory = QDir(installRoot).filePath(QStringLiteral("staging/package-fixture"));
        const QString packageRoot = QDir(stageDirectory).filePath(QStringLiteral("contents/package-root"));
        QDir().mkpath(packageRoot);
        const QJsonObject stage{{"schema", 1}, {"product", "Headroom"}, {"version", version}, {"platform", platform},
            {"architecture", architecture}, {"package_asset", "fixture-package"}, {"manifest_sha256", QString(64, 'a')}, {"package_root", packageRoot}};
        result["stage"] = stage;
        QFile verified(QDir(stageDirectory).filePath(QStringLiteral("verified-stage.json")));
        if (verified.open(QIODevice::WriteOnly | QIODevice::Truncate)) verified.write(qEnvironmentVariableIsSet("HEADROOM_UPDATE_FIXTURE_BAD_STAGE") ? QByteArray("{}") : QJsonDocument(stage).toJson());
    }
    QTextStream(stdout) << QJsonDocument(QJsonObject{{"ok", true}, {"command", command}, {"result", result}}).toJson(QJsonDocument::Compact) << '\n';
    return 0;
}
