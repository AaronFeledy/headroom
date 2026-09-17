#include "updateservice.h"

#include <QCoreApplication>
#include <QCryptographicHash>
#include <QDesktopServices>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QProcessEnvironment>
#include <QRegularExpression>
#include <QSaveFile>
#include <QStandardPaths>
#include <QSysInfo>
#include <QUrl>
#include <QSet>
#include <utility>
#include <memory>

namespace {
constexpr qsizetype maximumToolOutput = 256 * 1024;

QString cleanAbsolute(const QString &path) {
    const QString normalized = QDir::fromNativeSeparators(path);
    if (normalized.isEmpty() || !QDir::isAbsolutePath(normalized) || QDir::cleanPath(normalized) != normalized
        || normalized.contains('\n') || normalized.contains('\r') || normalized.contains(QChar::Null)) return {};
    return QDir::toNativeSeparators(normalized);
}

// Public update tooling resolves releases and packages only. It never needs
// the server bearer token, the server configuration, or the credential
// snapshot root, so every public child starts without them.
QProcessEnvironment publicToolEnvironment() {
    auto environment = QProcessEnvironment::systemEnvironment();
    for (const auto &name : {QStringLiteral("USAGE_AUTH_TOKEN"), QStringLiteral("USAGE_CONFIG"),
                             QStringLiteral("HEADROOM_CREDENTIAL_SNAPSHOT_ROOT")}) environment.remove(name);
    return environment;
}

QByteArray digest(const QString &path) {
    QFile file(path);
    if (!file.open(QIODevice::ReadOnly)) return {};
    QCryptographicHash hash(QCryptographicHash::Sha256);
    if (!hash.addData(&file)) return {};
    return hash.result();
}

QString recognizedToolFailure(const QJsonObject &object, const QString &command) {
    if (object.value(QStringLiteral("ok")) != QJsonValue(false)
        || object.value(QStringLiteral("command")).toString() != command) return {};
    const QString error = object.value(QStringLiteral("error")).toString();
    // Tool errors may contain signed download URLs, proxy credentials, paths,
    // or server environment values. Classify them; never display or log them.
    const auto http = QRegularExpression(QStringLiteral("^release server returned HTTP ([1-5][0-9]{2})$")).match(error);
    if (http.hasMatch()) {
        const int code = http.captured(1).toInt();
        const QString prefix = QStringLiteral("The release service returned HTTP %1.").arg(code);
        if (code >= 300 && code < 400)
            return prefix + QStringLiteral(" Its address has moved; run the current Headroom installer to update.");
        if (code == 403 || code == 429)
            return prefix + QStringLiteral(" Access may be blocked or rate limited. Try again later.");
        return prefix + QStringLiteral(" Try again later.");
    }
    if (error.startsWith(QStringLiteral("download release data: "))) {
        if (error.contains(QStringLiteral("x509:")) || error.contains(QStringLiteral("tls:")))
            return QStringLiteral("The update download could not establish a trusted TLS connection. Check certificates, system time, and any HTTPS proxy.");
        if (error.contains(QStringLiteral("no such host")))
            return QStringLiteral("The release service address could not be resolved. Check your network and DNS settings.");
        if (error.contains(QStringLiteral("context deadline exceeded")) || error.contains(QStringLiteral("timeout"), Qt::CaseInsensitive))
            return QStringLiteral("The release service request timed out. Check your connection and try again later.");
        return QStringLiteral("The release service could not be reached. Check your connection and any proxy settings.");
    }
    if (error == QStringLiteral("downloaded package checksum does not match release manifest")
        || error == QStringLiteral("downloaded package size does not match release manifest"))
        return QStringLiteral("The downloaded package failed integrity verification. The current installation was not changed.");
    if (error == QStringLiteral("Headroom installation identity is not valid"))
        return QStringLiteral("The installed package identity could not be verified. Run the current Headroom installer to repair it.");
    return {};
}
}

UpdateService::UpdateService(bool allowPublicTraffic, UpdateServiceOptions options, QObject *parent)
    : QObject(parent), m_options(std::move(options)), m_allowed(allowPublicTraffic), m_sessionAllowed(allowPublicTraffic) {
    m_timeout.setSingleShot(true);
    if (m_options.installRoot.isEmpty()) m_options.installRoot = qEnvironmentVariable("HEADROOM_INSTALL_ROOT");
    if (m_options.launcherPath.isEmpty()) m_options.launcherPath = qEnvironmentVariable("HEADROOM_LAUNCHER_PATH");
    if (m_options.packageVersion.isEmpty()) m_options.packageVersion = qEnvironmentVariable("HEADROOM_PACKAGE_VERSION");
#ifdef HEADROOM_SYSTEM_MANAGED
    m_options.systemManaged = true;
#endif
    if (m_options.managerPath.isEmpty()) {
#ifdef Q_OS_WIN
        m_options.managerPath = QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("headroom-package.exe"));
#elif defined(Q_OS_MACOS)
        m_options.managerPath = QDir(QCoreApplication::applicationDirPath()).absoluteFilePath(QStringLiteral("../../../bin/headroom-package"));
#else
        m_options.managerPath = QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("headroom-package"));
#endif
    }
    m_method = m_options.systemManaged ? QStringLiteral("system") : QStringLiteral("source");
    if (!m_allowed) {
        m_status = QStringLiteral("Update checks are disabled for this session.");
        log(m_status);
        return;
    }
    if (m_options.systemManaged) {
        m_status = QStringLiteral("This installation is managed by your system package manager.");
        log(m_status);
        return;
    }
    if (m_options.installRoot.isEmpty() || m_options.launcherPath.isEmpty() || m_options.packageVersion.isEmpty()) {
        m_status = QStringLiteral("This source installation is updated from its source checkout.");
        log(m_status);
        return;
    }
    inspectInstallation();
}

UpdateService::~UpdateService() {
    if (m_pairProcess) {
        m_pairProcess->disconnect(this);
        m_pairProcess->kill();
        m_pairProcess->waitForFinished(m_options.cancelGraceMs);
    }
    if (m_process) {
        m_process->disconnect(this);
        m_process->closeWriteChannel();
        if (!m_process->waitForFinished(m_options.cancelGraceMs)) m_process->kill();
    }
}

void UpdateService::inspectInstallation() { run(Operation::Inspect, QStringLiteral("inspect")); }

void UpdateService::startAutomaticCheck() {
    if (m_autoStarted) return;
    m_autoStarted = true;
    if (!m_allowed || m_suppressAutomaticCheck) return;
    m_autoPending = true;
    if (!m_process && m_official) {
        m_autoPending = false;
        m_autoStage = true;
        run(Operation::Check, QStringLiteral("check-update"));
    }
}

void UpdateService::setPublicTrafficAllowed(bool allowed) {
    const bool effective = m_sessionAllowed && allowed;
    if (m_operation == Operation::Apply) {
        m_hasDeferredAllowed = true;
        m_deferredAllowed = effective;
        return;
    }
    if (effective == m_allowed) return;
    m_allowed = effective;
    m_autoPending = false; m_autoStage = false;
    if (!effective) {
        if (m_official) {
            if (m_operation == Operation::Stage) {
                m_prePauseState = QStringLiteral("available");
                m_prePauseStatus = QStringLiteral("Headroom %1 is available.").arg(m_latestVersion);
            } else if (m_operation == Operation::Repair) {
                m_prePauseState = QStringLiteral("current");
                m_prePauseStatus = QStringLiteral("The Headroom installation needs repair.");
            } else if (m_operation == Operation::Check) {
                m_prePauseState = QStringLiteral("current");
                m_prePauseStatus = m_repairable ? QStringLiteral("The Headroom installation needs repair.")
                                                : QStringLiteral("Headroom updates automatically after a startup check.");
            } else {
                m_prePauseState = m_state;
                m_prePauseStatus = m_status;
            }
        } else {
            m_prePauseState.clear(); m_prePauseStatus.clear();
        }
        if (m_pairProcess) m_pairProcess->kill();
        if (m_process) cancel();
        else finishCLIRequest(QStringLiteral("cancelled"), false);
        m_state = QStringLiteral("unavailable"); m_status = QStringLiteral("Update checks are paused for this session.");
        emit changed(); return;
    }
    if (m_process) { m_resumeAfterCancel = true; return; }
    restoreAllowedState();
}

void UpdateService::checkForUpdates() {
    if (!canCheck()) return;
    m_suppressAutomaticCheck = false;
    m_autoStage = false;
    run(Operation::Check, QStringLiteral("check-update"));
}

void UpdateService::requestCLIUpdate(const QJsonObject &request, std::function<void(const QJsonObject &)> reply) {
    const QString nonce = request.value(QStringLiteral("request_nonce")).toString();
    const auto reject = [&](const QString &reason) { reply({{"ok", false}, {"status", reason}, {"request_nonce", nonce}}); };
    if (request.value(QStringLiteral("schema")).toInt() != 1 || request.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")
        || !QRegularExpression(QStringLiteral("^[0-9a-f]{48}$")).match(nonce).hasMatch()
        || cleanAbsolute(request.value(QStringLiteral("install_root")).toString()) != cleanAbsolute(m_options.installRoot)
        || m_options.installRoot.isEmpty()) { reject(QStringLiteral("invalid_request")); return; }
    if (!m_allowed || !m_official || m_options.systemManaged) { reject(QStringLiteral("unmanaged_installation")); return; }
    // A paired coordinator started by this desktop calls back here after both
    // systems are staged. Its separate process is expected at this point.
    if (m_cliReply || m_process) { reject(QStringLiteral("busy")); return; }
    const auto participants = request.value(QStringLiteral("additional_processes")).toArray();
    if (participants.isEmpty() || participants.size() > 3) { reject(QStringLiteral("invalid_participants")); return; }
    QSet<QString> roles;
    for (const auto &value : participants) {
        const auto participant = value.toObject();
        const QString role = participant.value(QStringLiteral("role")).toString();
        if ((role != QStringLiteral("cli") && role != QStringLiteral("public_launcher") && role != QStringLiteral("managed_server")) || roles.contains(role)
            || participant.size() != 3 || participant.value(QStringLiteral("pid")).toInteger() <= 0
            || cleanAbsolute(participant.value(QStringLiteral("executable")).toString()).isEmpty()) {
            reject(QStringLiteral("invalid_participants")); return;
        }
        roles.insert(role);
    }
    if (!roles.contains(QStringLiteral("cli"))) { reject(QStringLiteral("invalid_participants")); return; }
    if (m_pairProcess) {
        const auto participant = participants.first().toObject();
        if (participants.size() != 1 || participant.value(QStringLiteral("role")).toString() != QStringLiteral("cli")
            || m_pairProcess->processId() <= 0 || participant.value(QStringLiteral("pid")).toInteger() != m_pairProcess->processId()
            || cleanAbsolute(participant.value(QStringLiteral("executable")).toString()) != cleanAbsolute(m_pairProcess->program())) {
            reject(QStringLiteral("busy")); return;
        }
    }
    m_cliReply = std::move(reply); m_cliRequestNonce = nonce; m_cliParticipants = participants;
    m_suppressAutomaticCheck = false;
    // The manager authenticates every process against its inventoried executable
    // and kernel start identity before the desktop authorizes any process exit.
    m_autoStage = true;
    if (request.contains(QStringLiteral("prepared_stage"))) {
        const auto stage = request.value(QStringLiteral("prepared_stage")).toObject();
        handleUpdateResult(Operation::Stage, {{"status", "staged"}, {"current_version", m_options.packageVersion},
            {"version", stage.value("version")}, {"platform", m_platform}, {"architecture", m_architecture},
            {"asset_name", stage.value("package_asset")}, {"stage", stage}});
        return;
    }
    run(Operation::Check, QStringLiteral("check-update"));
}

void UpdateService::finishCLIRequest(const QString &status, bool ok) {
    if (!m_cliReply) return;
    auto reply = std::move(m_cliReply);
    const auto nonce = std::exchange(m_cliRequestNonce, {});
    m_cliParticipants = {};
    reply({{"ok", ok}, {"status", status}, {"request_nonce", nonce},
           {"version", m_latestVersion.isEmpty() ? m_options.packageVersion : m_latestVersion}});
}

void UpdateService::stageUpdate() {
    if (!canStage()) return;
    run(Operation::Stage, QStringLiteral("stage-update"));
}

void UpdateService::repairInstallation() {
    if (!canRepair()) return;
    run(Operation::Repair, QStringLiteral("stage-repair"));
}

void UpdateService::restartToApply() {
    if (!restartAvailable() || m_verifiedStage.isEmpty() || m_process) return;
    if (!m_cliReply && !m_stageIsRepair && QFileInfo::exists(QDir(m_options.installRoot).filePath(QStringLiteral("pairing/windows-wsl.json")))) {
        startPairedUpdate(); return;
    }
    const QString packageRoot = cleanAbsolute(m_verifiedStage.value(QStringLiteral("package_root")).toString());
    if (packageRoot.isEmpty()) { fail(QStringLiteral("The verified update stage was not recognized.")); return; }
    const QString stageRecord = QDir(packageRoot).absoluteFilePath(QStringLiteral("../../verified-stage.json"));
    QStringList arguments{QStringLiteral("prepare-apply"), QStringLiteral("--install-root"), m_options.installRoot,
        QStringLiteral("--entry-path"), m_options.launcherPath, QStringLiteral("--stage-record"), QDir::cleanPath(stageRecord),
        QStringLiteral("--current-pid"), QString::number(QCoreApplication::applicationPid()),
        QStringLiteral("--current-executable"), QCoreApplication::applicationFilePath()};
    if (m_ownedProcessProvider) {
        const auto owned = m_ownedProcessProvider();
        if (owned.first > 0 && !owned.second.isEmpty()) arguments << QStringLiteral("--owned-child-pid") << QString::number(owned.first)
            << QStringLiteral("--owned-child-executable") << owned.second;
    }
    for (const auto &argument : m_relaunchArguments)
        arguments << QStringLiteral("--relaunch-arg") << argument;
    for (const auto &participant : m_cliParticipants)
        arguments << QStringLiteral("--participant") << QString::fromUtf8(QJsonDocument(participant.toObject()).toJson(QJsonDocument::Compact));
    run(Operation::Apply, QStringLiteral("prepare-apply"), arguments);
}

void UpdateService::startPairedUpdate() {
    if (m_pairProcess) return;
    m_diagnosticCommand = QStringLiteral("paired-update");
    // This is a UI readiness check, not authorization. The coordinator still
    // validates the complete private pairing record and reciprocal identity.
    QFile pairing(QDir(m_options.installRoot).filePath(QStringLiteral("pairing/windows-wsl.json")));
    QJsonParseError pairingError;
    const auto pairingDocument = !QFileInfo(pairing).isSymLink() && pairing.open(QIODevice::ReadOnly)
        && pairing.size() <= 32 * 1024 ? QJsonDocument::fromJson(pairing.readAll(), &pairingError) : QJsonDocument();
    const auto pairingObject = pairingDocument.object();
    if (pairingError.error != QJsonParseError::NoError || !pairingDocument.isObject()
        || pairingObject.value(QStringLiteral("schema")).toInt() != 1
        || pairingObject.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")
        || pairingObject.value(QStringLiteral("state")).toString() != QStringLiteral("active")) {
        m_status = QStringLiteral("Windows/WSL pairing needs attention. Complete pairing, or run headroom update --this-install-only for a local update. The downloaded package is still available.");
        log(QStringLiteral("Paired update blocked: pairing is incomplete or invalid; the verified package was retained."));
        emit changed(); return;
    }
    if (!m_cliAvailable) { fail(QStringLiteral("Rerun the Headroom installer to enable updates for this paired installation.")); return; }
    // inspect verified the complete generation, including this CLI payload.
    // The CLI revalidates its own installed identity before any public request.
    auto process = new QProcess(this);
    m_pairProcess = process;
    const QString application = m_options.applicationPath.isEmpty() ? QCoreApplication::applicationFilePath() : m_options.applicationPath;
    process->setProgram(QDir(QFileInfo(application).absolutePath()).filePath(
#ifdef Q_OS_WIN
        QStringLiteral("headroom-cli.exe")));
    process->setCreateProcessArgumentsModifier([](QProcess::CreateProcessArguments *arguments) { arguments->flags |= 0x08000000; });
#else
        QStringLiteral("headroom-cli")));
#endif
    process->setArguments({QStringLiteral("update")});
    auto environment = publicToolEnvironment();
    environment.remove(QStringLiteral("HEADROOM_PUBLIC_LAUNCHER_PID"));
    environment.remove(QStringLiteral("HEADROOM_PUBLIC_LAUNCHER_PATH"));
    environment.remove(QStringLiteral("HEADROOM_PUBLIC_LAUNCHER_TOKEN"));
    process->setProcessEnvironment(environment);
    process->setProcessChannelMode(QProcess::MergedChannels);
    auto count = std::make_shared<qint64>(0);
    connect(process, &QProcess::readyReadStandardOutput, this, [process, count] {
        *count += process->readAllStandardOutput().size();
        if (*count > maximumToolOutput) process->kill();
    });
    const auto finish = [this, process](bool success) {
        if (m_pairProcess != process) return;
        m_pairProcess = nullptr; process->deleteLater();
        if (m_cliReply || m_process) return;
        if (!m_allowed) { m_state = QStringLiteral("unavailable"); m_status = QStringLiteral("Update checks are paused for this session."); emit changed(); return; }
        if (!success) {
            // Pairing failure does not invalidate the locally verified archive.
            // Keep it available while the user repairs the pairing or retries.
            m_state = QStringLiteral("staged");
            m_status = QStringLiteral("The Windows/WSL update did not finish. Run headroom update in a terminal for recovery details. The downloaded package is still available.");
            log(QStringLiteral("Paired Windows/WSL update failed; the verified package was retained. Run headroom update for recovery details."));
            emit changed(); return;
        }
        m_verifiedStage = {}; m_state = QStringLiteral("current");
        log(QStringLiteral("Paired Windows/WSL update completed."));
        m_status = QStringLiteral("The paired installations are up to date."); emit changed();
    };
    connect(process, &QProcess::finished, this, [finish](int code, QProcess::ExitStatus status) { finish(code == 0 && status == QProcess::NormalExit); });
    connect(process, &QProcess::errorOccurred, this, [finish](QProcess::ProcessError error) { if (error == QProcess::FailedToStart) finish(false); });
    QTimer::singleShot(15 * 60 * 1000, process, [process] { if (process->state() != QProcess::NotRunning) process->kill(); });
    m_status = QStringLiteral("Preparing the Windows desktop and WSL server update…"); emit changed();
    log(QStringLiteral("Paired Windows/WSL update started."));
    process->start();
}

void UpdateService::cancel() {
    if (!m_process || m_operation == Operation::Apply) return;
    m_cancelRequested = true;
    log(QStringLiteral("Cancellation requested."));
    auto process = m_process;
    process->closeWriteChannel();
    QTimer::singleShot(m_options.cancelGraceMs, process, [process] {
        if (process->state() != QProcess::NotRunning) process->kill();
    });
}

void UpdateService::run(Operation operation, const QString &command, const QStringList &explicitArguments) {
    if (m_process) return;
    if (operation != Operation::Inspect && !m_allowed) return;
    auto process = new QProcess(this);
    m_process = process;
    m_operation = operation;
    m_diagnosticCommand = command;
    log(QStringLiteral("Started."));
    m_output.clear(); m_errorOutput.clear(); m_cancelRequested = false; m_timedOut = false;
    QStringList arguments;
    if (operation == Operation::Apply) {
        arguments = explicitArguments;
    } else {
        arguments = {command, QStringLiteral("--install-root"), m_options.installRoot};
        if (operation != Operation::Inspect) arguments.append(QStringLiteral("--cancel-stdin"));
    }
    process->setProgram(m_options.managerPath);
    process->setArguments(arguments);
    process->setProcessChannelMode(QProcess::SeparateChannels);
    // Apply hands the approved server environment to the transaction manager.
    const auto environment = operation == Operation::Apply ? QProcessEnvironment::systemEnvironment() : publicToolEnvironment();
    process->setProcessEnvironment(environment);
#ifdef Q_OS_WIN
    if (operation == Operation::Apply)
        process->setCreateProcessArgumentsModifier([](QProcess::CreateProcessArguments *arguments) {
            arguments->flags |= 0x08000000; // CREATE_NO_WINDOW for the console-subsystem private manager.
        });
#endif
    if (operation == Operation::Check || operation == Operation::Inspect) {
        m_state = QStringLiteral("checking");
        m_status = operation == Operation::Inspect ? QStringLiteral("Checking installation…") : QStringLiteral("Checking for Headroom updates…");
    } else if (operation == Operation::Apply) {
        m_state = QStringLiteral("applying");
        m_status = QStringLiteral("Preparing a safe restart…");
    } else {
        m_state = QStringLiteral("downloading");
        m_status = operation == Operation::Repair ? QStringLiteral("Downloading a matching repair package…")
                                                   : QStringLiteral("Downloading and verifying the Headroom update…");
    }
    emit changed();
    connect(process, &QProcess::readyReadStandardOutput, this, [this, process] {
        m_output += process->readAllStandardOutput();
        if (m_output.size() > maximumToolOutput) process->kill();
    });
    connect(process, &QProcess::readyReadStandardError, this, [this, process] {
        m_errorOutput += process->readAllStandardError();
        if (m_errorOutput.size() > maximumToolOutput) process->kill();
    });
    connect(process, &QProcess::finished, this, [this, process, operation](int code, QProcess::ExitStatus status) {
        m_timeout.stop();
        m_output += process->readAllStandardOutput();
        m_errorOutput += process->readAllStandardError();
        m_process = nullptr;
        m_operation = Operation::None;
        process->deleteLater();
        finish(operation, code, status);
    });
    connect(process, &QProcess::errorOccurred, this, [this, process](QProcess::ProcessError error) {
        if (error == QProcess::FailedToStart && process == m_process) {
            m_timeout.stop(); m_process = nullptr; process->deleteLater();
            m_operation = Operation::None;
            if (!m_allowed) {
                m_resumeAfterCancel = false;
                m_state = QStringLiteral("unavailable");
                m_status = QStringLiteral("Update checks are paused for this session.");
                emit changed(); return;
            }
            if (m_resumeAfterCancel) { m_resumeAfterCancel = false; finishCLIRequest(QStringLiteral("cancelled"), false); restoreAllowedState(); return; }
            fail(QStringLiteral("The Headroom package service could not be started."));
        }
    });
    connect(&m_timeout, &QTimer::timeout, process, [this, process] {
        if (process != m_process) return;
        m_timedOut = true;
        process->closeWriteChannel();
        QTimer::singleShot(m_options.cancelGraceMs, process, [process] { if (process->state() != QProcess::NotRunning) process->kill(); });
    }, Qt::SingleShotConnection);
    process->start();
    m_timeout.start(m_options.timeoutMs);
}

void UpdateService::finish(Operation operation, int exitCode, QProcess::ExitStatus exitStatus) {
    if (!m_allowed) {
        m_resumeAfterCancel = false;
        m_autoStage = false;
        m_state = QStringLiteral("unavailable"); m_status = QStringLiteral("Update checks are paused for this session.");
        log(QStringLiteral("Stopped because update checks are paused for this session."));
        emit changed(); finishCLIRequest(QStringLiteral("cancelled"), false); return;
    }
    if (m_cancelRequested || m_timedOut) {
        if (m_resumeAfterCancel) { m_resumeAfterCancel = false; finishCLIRequest(QStringLiteral("cancelled"), false); restoreAllowedState(); return; }
        m_autoStage = false; m_verifiedStage = {};
        m_state = QStringLiteral("failed");
        m_status = m_timedOut ? QStringLiteral("The update operation timed out. Try again later.")
                              : QStringLiteral("The update operation was cancelled.");
        log(m_status);
		emit changed(); finishCLIRequest(QStringLiteral("cancelled"), false); applyDeferredTrafficState(); return;
    }
    if (m_output.size() > maximumToolOutput || m_errorOutput.size() > maximumToolOutput) {
        fail(QStringLiteral("The package service exceeded its output limit.")); return;
    }
    if (exitStatus != QProcess::NormalExit) {
        fail(QStringLiteral("The Headroom package service stopped unexpectedly.")); return;
    }
    if (exitCode != 0) {
        const auto object = QJsonDocument::fromJson(m_output).object();
        const QString detail = recognizedToolFailure(object, m_diagnosticCommand);
        log(QStringLiteral("Package service exited with code %1.").arg(exitCode));
        fail(detail.isEmpty() ? QStringLiteral("The update operation did not complete (package service exit code %1). Try again later.").arg(exitCode) : detail);
        return;
    }
    QJsonParseError error;
    const auto document = QJsonDocument::fromJson(m_output, &error);
    if (error.error != QJsonParseError::NoError || !document.isObject()) {
        fail(QStringLiteral("The package service returned an invalid result.")); return;
    }
    const auto object = document.object();
    QString expectedCommand;
    switch (operation) {
    case Operation::Inspect: expectedCommand = QStringLiteral("inspect"); break;
    case Operation::Check: expectedCommand = QStringLiteral("check-update"); break;
    case Operation::Stage: expectedCommand = QStringLiteral("stage-update"); break;
    case Operation::Repair: expectedCommand = QStringLiteral("stage-repair"); break;
    case Operation::Apply: expectedCommand = QStringLiteral("prepare-apply"); break;
    case Operation::None: break;
    }
    if (!object.value(QStringLiteral("ok")).toBool() || object.value(QStringLiteral("command")).toString() != expectedCommand
        || !object.value(QStringLiteral("result")).isObject()) {
        const QString detail = recognizedToolFailure(object, expectedCommand);
        fail(detail.isEmpty() ? QStringLiteral("The update package was rejected. The current installation was not changed.") : detail); return;
    }
    const auto result = object.value(QStringLiteral("result")).toObject();
    if (operation == Operation::Apply) {
        if (!authorizePreparedApply(result)) {
            fail(QStringLiteral("The update transaction was not accepted.")); return;
        }
        m_status = QStringLiteral("Restarting into the verified Headroom package…"); emit changed();
        log(QStringLiteral("Update transaction accepted; restarting into the verified package."));
        finishCLIRequest(QStringLiteral("accepted"), true);
        emit applyPrepared(); return;
    }
    if (operation == Operation::Inspect) handleInspection(result); else handleUpdateResult(operation, result);
}

bool UpdateService::authorizePreparedApply(const QJsonObject &result) const {
    if (result.value(QStringLiteral("schema")).toInt() != 1 || result.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")) return false;
    const QString root = cleanAbsolute(m_options.installRoot);
    const QString directory = cleanAbsolute(result.value(QStringLiteral("transaction_directory")).toString());
    const QString request = cleanAbsolute(result.value(QStringLiteral("request_path")).toString());
    const QString manager = cleanAbsolute(result.value(QStringLiteral("manager_path")).toString());
    const QString acknowledgement = cleanAbsolute(result.value(QStringLiteral("acknowledgement_path")).toString());
    const QString commit = cleanAbsolute(result.value(QStringLiteral("commit_path")).toString());
    const QString nonce = result.value(QStringLiteral("nonce")).toString();
    const auto equalsPath = [](const QString &left, const QString &right) {
        const QString cleanLeft = cleanAbsolute(left), cleanRight = cleanAbsolute(right);
        return !cleanLeft.isEmpty() && cleanLeft == cleanRight;
    };
    if (root.isEmpty() || directory.isEmpty() || request.isEmpty() || manager.isEmpty() || acknowledgement.isEmpty() || commit.isEmpty()
        || !QRegularExpression(QStringLiteral("^apply-[0-9a-f]{32}$")).match(QFileInfo(directory).fileName()).hasMatch()
        || QFileInfo(directory).dir().canonicalPath() != QFileInfo(QDir(root).filePath(QStringLiteral("transactions"))).canonicalFilePath()
        || !equalsPath(request, QDir(directory).filePath(QStringLiteral("apply-request.json")))
        || !equalsPath(acknowledgement, QDir(directory).filePath(QStringLiteral("accepted.json")))
        || !equalsPath(commit, QDir(directory).filePath(QStringLiteral("commit.json")))
        || !equalsPath(manager, QDir(directory).filePath(
#ifdef Q_OS_WIN
               QStringLiteral("headroom-apply.exe")))
#else
               QStringLiteral("headroom-apply")))
#endif
        || !QRegularExpression(QStringLiteral("^[0-9a-f]{48}$")).match(nonce).hasMatch()
        || !QFileInfo(request).isFile() || QFileInfo(request).isSymLink() || !QFileInfo(manager).isFile() || QFileInfo(manager).isSymLink()) return false;
    QFile requestFile(request);
    QJsonParseError requestError;
    const auto requestDocument = requestFile.open(QIODevice::ReadOnly) && requestFile.size() <= maximumToolOutput
        ? QJsonDocument::fromJson(requestFile.readAll(), &requestError) : QJsonDocument();
    if (requestError.error != QJsonParseError::NoError || !requestDocument.isObject()) return false;
    const auto requestObject = requestDocument.object();
    const QString packageRoot = cleanAbsolute(m_verifiedStage.value(QStringLiteral("package_root")).toString());
    const QString expectedStage = packageRoot.isEmpty() ? QString()
        : cleanAbsolute(QDir::cleanPath(QDir(packageRoot).absoluteFilePath(QStringLiteral("../../verified-stage.json"))));
    if (requestObject.value(QStringLiteral("schema")).toInt() != 1 || requestObject.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")
        || !equalsPath(requestObject.value(QStringLiteral("install_root")).toString(), root)
        || !equalsPath(requestObject.value(QStringLiteral("entry_path")).toString(), m_options.launcherPath)
        || expectedStage.isEmpty() || !equalsPath(requestObject.value(QStringLiteral("stage_record")).toString(), expectedStage)
        || requestObject.value(QStringLiteral("current_pid")).toInteger() != QCoreApplication::applicationPid()
        || !equalsPath(requestObject.value(QStringLiteral("current_executable")).toString(), QCoreApplication::applicationFilePath())
        || !equalsPath(requestObject.value(QStringLiteral("acknowledgement_path")).toString(), acknowledgement)
        || !equalsPath(requestObject.value(QStringLiteral("commit_path")).toString(), commit)
        || requestObject.value(QStringLiteral("nonce")).toString() != nonce) return false;
    const auto captured = requestObject.value(QStringLiteral("additional_processes")).toArray();
    if (captured.size() < m_cliParticipants.size() || captured.size() > 4
        || captured.size() > m_cliParticipants.size() + 2) return false;
    for (qsizetype index = 0; index < m_cliParticipants.size(); ++index) {
        const auto actual = captured.at(index).toObject(), expected = m_cliParticipants.at(index).toObject();
        if (actual.value(QStringLiteral("role")) != expected.value(QStringLiteral("role"))
            || actual.value(QStringLiteral("pid")) != expected.value(QStringLiteral("pid"))
            || !equalsPath(actual.value(QStringLiteral("executable")).toString(), expected.value(QStringLiteral("executable")).toString())
            || actual.value(QStringLiteral("process_token")).toString().isEmpty()) return false;
    }
    QSet<QString> automaticRoles;
    for (qsizetype index = m_cliParticipants.size(); index < captured.size(); ++index) {
        // The trusted manager may add a registered standalone server belonging
        // to this same installation. It verifies the private service receipt,
        // executable digest and kernel creation token before preparing it.
        const auto service = captured.at(index).toObject();
        const QString role = service.value(QStringLiteral("role")).toString();
        if (automaticRoles.contains(role)) return false;
        automaticRoles.insert(role);
        for (const auto &participant : m_cliParticipants)
            if (participant.toObject().value(QStringLiteral("role")).toString() == role) return false;
        const QString application = m_options.applicationPath.isEmpty() ? QCoreApplication::applicationFilePath() : m_options.applicationPath;
        const QString cli = QDir(QFileInfo(application).absolutePath()).filePath(
#ifdef Q_OS_WIN
            QStringLiteral("headroom-cli.exe"));
#else
            QStringLiteral("headroom-cli"));
#endif
        QString expected;
        if (role == QStringLiteral("managed_server")) expected = cli;
#ifdef Q_OS_WIN
        else if (role == QStringLiteral("managed_launcher")) expected = m_cliEntryPath;
#endif
        if (expected.isEmpty() || service.value(QStringLiteral("pid")).toInteger() <= 0
            || service.value(QStringLiteral("process_token")).toString().isEmpty()
            || !equalsPath(service.value(QStringLiteral("executable")).toString(), expected)) return false;
    }
    QFile accepted(acknowledgement);
    QJsonParseError parseError;
    const auto acceptedDocument = accepted.open(QIODevice::ReadOnly) && accepted.size() <= 4096
        ? QJsonDocument::fromJson(accepted.readAll(), &parseError) : QJsonDocument();
    if (parseError.error != QJsonParseError::NoError || !acceptedDocument.isObject()) return false;
    const auto acceptedObject = acceptedDocument.object();
    if (acceptedObject.value(QStringLiteral("schema")).toInt() != 1 || acceptedObject.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")
        || !acceptedObject.value(QStringLiteral("accepted")).toBool() || acceptedObject.value(QStringLiteral("nonce")).toString() != nonce) return false;
    QSaveFile authorization(commit);
    const QJsonObject value{{QStringLiteral("schema"), 1}, {QStringLiteral("product"), QStringLiteral("Headroom")},
        {QStringLiteral("commit"), true}, {QStringLiteral("nonce"), nonce}};
    if (!authorization.open(QIODevice::WriteOnly) || authorization.write(QJsonDocument(value).toJson(QJsonDocument::Compact) + '\n') < 0 || !authorization.commit()) return false;
#ifndef Q_OS_WIN
    QFile::setPermissions(commit, QFileDevice::ReadOwner | QFileDevice::WriteOwner);
#endif
    return true;
}

bool UpdateService::validateInstalledApplicationIdentity(const QJsonObject &result) const {
    if (m_options.fixtureIdentity) return result.value(QStringLiteral("trusted_identity")).toBool();
    const QString root = cleanAbsolute(m_options.installRoot);
    const QString version = result.value(QStringLiteral("version")).toString();
    const QString versionPath = result.value(QStringLiteral("version_path")).toString();
#ifdef Q_OS_WIN
    const QString nativePlatform = QStringLiteral("windows");
#elif defined(Q_OS_MACOS)
    const QString nativePlatform = QStringLiteral("macos");
#else
    const QString nativePlatform = QStringLiteral("linux");
#endif
    QString nativeArchitecture = QSysInfo::currentCpuArchitecture();
    if (nativeArchitecture == QStringLiteral("amd64")) nativeArchitecture = QStringLiteral("x86_64");
    if (nativeArchitecture == QStringLiteral("aarch64")) nativeArchitecture = QStringLiteral("arm64");
    if (root.isEmpty() || m_options.launcherPath.isEmpty() || version != m_options.packageVersion || versionPath.isEmpty()
        || QDir::isAbsolutePath(versionPath) || versionPath.contains(QLatin1Char('\\')) || QDir::cleanPath(versionPath) != versionPath
        || !versionPath.startsWith(QStringLiteral("versions/"))
        || version != QCoreApplication::applicationVersion() || result.value(QStringLiteral("platform")).toString() != nativePlatform
        || result.value(QStringLiteral("architecture")).toString() != nativeArchitecture
        || !result.value(QStringLiteral("trusted_identity")).toBool()) return false;
#ifdef Q_OS_WIN
    const QString relativeApplication = QStringLiteral("bin/headroom.exe");
#elif defined(Q_OS_MACOS)
    const QString relativeApplication = QStringLiteral("Headroom.app/Contents/MacOS/headroom");
#else
    const QString relativeApplication = QStringLiteral("bin/headroom");
#endif
    const QString expectedApp = QDir(root).filePath(versionPath + QLatin1Char('/') + relativeApplication);
    const QString runningApplication = m_options.applicationPath.isEmpty() ? QCoreApplication::applicationFilePath() : m_options.applicationPath;
    return QFileInfo(runningApplication).canonicalFilePath() == QFileInfo(expectedApp).canonicalFilePath();
}

bool UpdateService::validateIdentity(const QJsonObject &result) const {
    if (!validateInstalledApplicationIdentity(result)) return false;
    if (m_options.fixtureIdentity) return true;
    const QString root = cleanAbsolute(m_options.installRoot);
    const QString launcher = cleanAbsolute(m_options.launcherPath);
    QFile association(launcher + QStringLiteral(".root"));
    if (!association.open(QIODevice::ReadOnly) || association.size() > 4096
        || QString::fromUtf8(association.readAll()) != QDir::toNativeSeparators(root) + QLatin1Char('\n')) return false;
    const QString internalLauncher = cleanAbsolute(result.value(QStringLiteral("launcher_path")).toString());
    if (internalLauncher.isEmpty()) return false;
    const QFileInfo externalInfo(launcher), internalInfo(internalLauncher);
    if (!externalInfo.isFile() || externalInfo.isSymLink() || !internalInfo.isFile() || internalInfo.isSymLink()) return false;
    return digest(launcher) == digest(internalLauncher) && !digest(launcher).isEmpty();
}

void UpdateService::handleInspection(const QJsonObject &result) {
    if (!validateIdentity(result)) {
        m_state = QStringLiteral("unavailable");
        m_status = m_options.systemManaged
            ? QStringLiteral("This installation is managed by your system package manager.")
            : validateInstalledApplicationIdentity(result)
                ? QStringLiteral("The Headroom launcher is damaged. Rerun the official installer to repair this installation.")
                : QStringLiteral("This source installation is updated from its source checkout.");
        log(m_status);
        emit changed(); return;
    }
    if (result.value(QStringLiteral("complete")).toBool())
        m_trustedLauncherPath = cleanAbsolute(result.value(QStringLiteral("launcher_path")).toString());
    m_official = true; m_method = QStringLiteral("automatic");
    m_cliAvailable = result.value(QStringLiteral("complete")).toBool() && !result.value(QStringLiteral("cli_entry_path")).toString().isEmpty();
    m_cliEntryPath = cleanAbsolute(result.value(QStringLiteral("cli_entry_path")).toString());
    m_platform = result.value(QStringLiteral("platform")).toString();
    m_architecture = result.value(QStringLiteral("architecture")).toString();
    m_packageKind = result.value(QStringLiteral("package_kind")).toString();
    const auto missing = result.value(QStringLiteral("missing")).toArray();
    for (const auto &item : missing) {
        const QString path = item.toString();
        if (path == QStringLiteral("bin/usage-server") || path == QStringLiteral("bin/usage-server.exe")
            || path == QStringLiteral("Headroom.app/Contents/MacOS/usage-server")
            || path == QStringLiteral("bin/headroom-credential-helper.exe") || path == QStringLiteral("bootstrap/headroom")
            || path == QStringLiteral("bootstrap/headroom.exe") || path == QStringLiteral("bootstrap/headroom-package")
            || path == QStringLiteral("bootstrap/headroom-package.exe") || path == QStringLiteral("bootstrap/association")) m_repairable = true;
    }
    if (result.value(QStringLiteral("apply_status")).toString() == QStringLiteral("rolled_back")) {
        log(QStringLiteral("The previous update was rolled back; the prior installation was restored."));
	    m_suppressAutomaticCheck = true;
        m_state = QStringLiteral("failed");
        m_status = QStringLiteral("The update could not start, so Headroom restored the previous installation. %1")
                       .arg(result.value(QStringLiteral("apply_message")).toString());
    } else if (result.value(QStringLiteral("apply_status")).toString() == QStringLiteral("recovery_required")) {
        log(QStringLiteral("Update recovery is incomplete. Restart Headroom to retry recovery, or rerun the installer."));
        m_suppressAutomaticCheck = true;
        m_state = QStringLiteral("failed");
        m_status = QStringLiteral("Headroom could not finish restoring the previous installation. Restart Headroom to retry recovery; if the problem remains, rerun the installer. %1")
                       .arg(result.value(QStringLiteral("apply_message")).toString());
    } else {
        m_state = QStringLiteral("current");
        m_status = result.value(QStringLiteral("apply_status")).toString() == QStringLiteral("applied")
            ? QStringLiteral("Headroom updated successfully.")
            : m_repairable ? QStringLiteral("The Headroom installation needs repair.")
                           : QStringLiteral("Headroom updates automatically after a startup check.");
        log(result.value(QStringLiteral("apply_status")).toString() == QStringLiteral("applied")
            ? QStringLiteral("Update completed successfully.")
            : m_repairable ? QStringLiteral("Installation verified; repair is needed.")
                           : QStringLiteral("Installation verified; automatic updates are available."));
    }
    emit changed();
    if (m_autoPending) {
        m_autoPending = false;
        if (!m_suppressAutomaticCheck) { m_autoStage = true; run(Operation::Check, QStringLiteral("check-update")); }
    }
}

void UpdateService::restoreAllowedState() {
    if (!m_allowed) return;
    if (m_official) {
        if (!m_verifiedStage.isEmpty()) {
            m_state = QStringLiteral("staged");
            m_status = QStringLiteral("A verified Headroom package is staged. Restart to apply it.");
        } else if (!m_prePauseState.isEmpty()) {
            m_state = m_prePauseState;
            m_status = m_prePauseStatus;
        } else {
            m_state = QStringLiteral("current");
            m_status = m_repairable ? QStringLiteral("The Headroom installation needs repair.")
                                    : QStringLiteral("Headroom updates automatically after a startup check.");
        }
        m_prePauseState.clear(); m_prePauseStatus.clear();
        emit changed(); return;
    }
    if (!m_options.installRoot.isEmpty() && !m_options.launcherPath.isEmpty() && !m_options.packageVersion.isEmpty()
        && !m_options.systemManaged) { inspectInstallation(); return; }
    m_state = QStringLiteral("unavailable");
    m_status = m_options.systemManaged ? QStringLiteral("This installation is managed by your system package manager.")
                                       : QStringLiteral("This source installation is updated from its source checkout.");
    emit changed();
}

void UpdateService::handleUpdateResult(Operation operation, const QJsonObject &result) {
    const QString status = result.value(QStringLiteral("status")).toString();
    const QString current = result.value(QStringLiteral("current_version")).toString();
    const QString version = result.value(QStringLiteral("version")).toString();
    if (!m_official || current != m_options.packageVersion
        || result.value(QStringLiteral("platform")).toString() != m_platform
        || result.value(QStringLiteral("architecture")).toString() != m_architecture ||
        (status != QStringLiteral("unavailable") && status != QStringLiteral("current")
         && status != QStringLiteral("available") && status != QStringLiteral("staged"))) {
        fail(QStringLiteral("The package service returned an inconsistent update result.")); return;
    }
    m_latestVersion = version;
    if (status == QStringLiteral("available") && operation == Operation::Check && m_autoStage) {
        log(QStringLiteral("Check completed: a newer package is available."));
        m_autoStage = false;
        m_state = QStringLiteral("available"); emit changed();
        run(Operation::Stage, QStringLiteral("stage-update")); return;
    }
    m_autoStage = false;
    if (status == QStringLiteral("staged")) {
        const auto stage = result.value(QStringLiteral("stage")).toObject();
        const QString packageRoot = cleanAbsolute(stage.value(QStringLiteral("package_root")).toString());
        const QString stagingRoot = QFileInfo(QDir(m_options.installRoot).filePath(QStringLiteral("staging"))).canonicalFilePath();
        const QString canonicalPackage = QFileInfo(packageRoot).canonicalFilePath();
        const QString prefix = QDir::fromNativeSeparators(stagingRoot) + QLatin1Char('/');
        const QFileInfo packageInfo(packageRoot);
        const QString stageDirectory = QDir::cleanPath(packageInfo.dir().absoluteFilePath(QStringLiteral("..")));
        QFile record(QDir(stageDirectory).filePath(QStringLiteral("verified-stage.json")));
        QJsonDocument recorded;
        if (record.open(QIODevice::ReadOnly) && record.size() <= maximumToolOutput) recorded = QJsonDocument::fromJson(record.readAll());
        if (stage.value(QStringLiteral("schema")).toInt() != 1 || stage.value(QStringLiteral("product")).toString() != QStringLiteral("Headroom")
            || stage.value(QStringLiteral("package_kind")).toString() != m_packageKind
            || stage.value(QStringLiteral("version")).toString() != version
            || stage.value(QStringLiteral("platform")).toString() != m_platform
            || stage.value(QStringLiteral("architecture")).toString() != m_architecture
            || stage.value(QStringLiteral("package_asset")).toString() != result.value(QStringLiteral("asset_name")).toString()
            || packageRoot.isEmpty() || stagingRoot.isEmpty() || canonicalPackage.isEmpty()
            || !QDir::fromNativeSeparators(canonicalPackage).startsWith(prefix)
            || !packageInfo.isDir() || packageInfo.isSymLink() || !recorded.isObject() || recorded.object() != stage) {
            fail(QStringLiteral("The verified update stage was not recognized.")); return;
        }
        m_state = QStringLiteral("staged");
        m_verifiedStage = stage;
        m_stageIsRepair = operation == Operation::Repair;
        m_status = operation == Operation::Repair ? QStringLiteral("A matching repair package is staged. Restart to apply it.")
                                                  : QStringLiteral("Headroom %1 is staged. Restart to apply it.").arg(version);
        log(QStringLiteral("Download and verification completed; the package is staged for restart."));
    } else if (status == QStringLiteral("available")) {
        m_state = QStringLiteral("available");
        m_status = QStringLiteral("Headroom %1 is available.").arg(version);
        log(QStringLiteral("Check completed: a newer package is available."));
    } else if (status == QStringLiteral("current")) {
        m_state = QStringLiteral("current");
        m_status = QStringLiteral("This version of Headroom is current.");
        log(QStringLiteral("Check completed: this installation is current."));
    } else {
        m_state = QStringLiteral("unavailable");
        m_status = QStringLiteral("No compatible Headroom package is published yet.");
        log(QStringLiteral("Check completed: no compatible package is published."));
    }
    emit changed();
    if (m_cliReply) {
        if (m_state == QStringLiteral("staged")) QTimer::singleShot(0, this, &UpdateService::restartToApply);
        else finishCLIRequest(status, status == QStringLiteral("current"));
    }
}

void UpdateService::fail(const QString &message) {
    log(message);
    m_autoStage = false;
    finishCLIRequest(QStringLiteral("failed"), false);
    m_verifiedStage = {};
	m_state = QStringLiteral("failed"); m_status = message; emit changed(); applyDeferredTrafficState();
}

void UpdateService::log(const QString &message) const {
    if (m_options.diagnostic)
        m_options.diagnostic(m_diagnosticCommand.isEmpty() ? message : m_diagnosticCommand + QStringLiteral(": ") + message);
}

void UpdateService::applyDeferredTrafficState() {
    if (!m_hasDeferredAllowed) return;
    const bool desired = m_deferredAllowed;
    m_hasDeferredAllowed = false;
    setPublicTrafficAllowed(desired);
}

QString UpdateService::guidePath() const {
    const QString appDir = QCoreApplication::applicationDirPath();
    const QStringList candidates {
#ifdef Q_OS_MACOS
        QDir(appDir).filePath(QStringLiteral("../Resources/update-guide.html")),
        QDir(appDir).filePath(QStringLiteral("../../../share/headroom/update-guide.html")),
#endif
        QDir(appDir).filePath(QStringLiteral("../share/headroom/update-guide.html")),
        QStandardPaths::locate(QStandardPaths::GenericDataLocation, QStringLiteral("headroom/update-guide.html")),
        QDir(appDir).filePath(QStringLiteral("../update-guide.html"))};
    for (const auto &candidate : candidates) if (!candidate.isEmpty() && QFileInfo::exists(candidate)) return QFileInfo(candidate).absoluteFilePath();
    return {};
}

void UpdateService::openUpdateMethod() {
    const QString path = guidePath();
    if (!path.isEmpty() && QDesktopServices::openUrl(QUrl::fromLocalFile(path))) return;
    fail(QStringLiteral("Could not open the installed update instructions."));
}
