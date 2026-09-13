#include "remoteupdate.h"

#include <QJsonDocument>
#include <QJsonObject>
#include <QProcessEnvironment>
#include <QRegularExpression>
#include <QStandardPaths>

namespace {
constexpr qsizetype maximumOutput = 64 * 1024;

QString acceptedVersion(const QByteArray &output) {
    // These are the public CLI's authored responses, including the v2.0.1 CLI.
    // A zero SSH exit alone is insufficient: a forced usage-only command can
    // return an unrelated success response. Never display raw remote output.
    const QString text = QString::fromUtf8(output).trimmed();
    const QString version = QStringLiteral("((?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?)");
    for (const QString &pattern : {
             QStringLiteral("\\AHeadroom ") + version + QStringLiteral(" (?:is current\\.|is staged and will finish installing now\\.|update accepted\\.)\\z"),
             QStringLiteral("\\AHeadroom update (?:accepted|current) \\(") + version + QStringLiteral("\\)\\.\\z")}) {
        const auto match = QRegularExpression(pattern).match(text);
        if (match.hasMatch() && match.captured(1).size() <= 80) return match.captured(1);
    }
    return {};
}
}

RemoteUpdateService::RemoteUpdateService(RemoteUpdateOptions options, QObject *parent)
    : QObject(parent), m_options(std::move(options)), m_network(m_options.ssh) {
    m_deadline.setSingleShot(true);
    m_poll.setSingleShot(true);
    connect(&m_deadline, &QTimer::timeout, this, [this] {
        fail(m_state == "verifying"
            ? QStringLiteral("The server has not reported the expected version. The update may still finish or may have rolled back. Check headroom update on the server; unmanaged services need a manual update.")
            : QStringLiteral("The remote update timed out. Its outcome is unknown and it may still finish. Check the server before trying again."));
    });
    connect(&m_poll, &QTimer::timeout, this, &RemoteUpdateService::verifyServer);
}

RemoteUpdateService::~RemoteUpdateService() { stopWork(); }

void RemoteUpdateService::setBackend(const QString &address) {
    QUrl normalized;
    SshTransport::parseAddress(address, &normalized);
    if (m_address == normalized) return;
    const bool interrupted = busy();
    stopWork();
    m_address = normalized;
    m_targetVersion.clear();
    m_state = interrupted ? QStringLiteral("failed") : QStringLiteral("idle");
    m_status = interrupted
        ? QStringLiteral("The connection changed before the previous server update could be confirmed. That update may still finish; check the previous server.")
        : available() ? QStringLiteral("Update the Headroom installation on this SSH host. Its managed server restarts when an update is applied.") : QString();
    if (interrupted) log(m_status);
    emit changed();
}

void RemoteUpdateService::setEnabled(bool enabled) {
    if (m_enabled == enabled) return;
    m_enabled = enabled;
    emit changed();
}

void RemoteUpdateService::start() {
    if (!canStart()) return;
    m_state = QStringLiteral("running");
    m_status = QStringLiteral("Checking and updating the remote server over SSH…");
    m_targetVersion.clear(); m_output.clear(); m_errorBytes = 0;
    log(m_status);
    auto process = new QProcess(this);
    m_process = process;
    process->setProgram(m_options.ssh.executablePath.isEmpty()
        ? QStandardPaths::findExecutable(QStringLiteral("ssh")) : m_options.ssh.executablePath);
    process->setArguments(SshTransport::updateArguments(m_address));
    auto environment = QProcessEnvironment::systemEnvironment();
    for (const auto &key : {"USAGE_AUTH_TOKEN", "USAGE_CONFIG", "HEADROOM_AUTH_TOKEN", "HEADROOM_AUTH_TOKEN_FILE", "HEADROOM_CREDENTIAL_SNAPSHOT_ROOT"})
        environment.remove(QString::fromLatin1(key));
    process->setProcessEnvironment(environment);
    process->setProcessChannelMode(QProcess::SeparateChannels);
    connect(process, &QProcess::readyReadStandardOutput, this, [this, process] {
        m_output += process->readAllStandardOutput();
        if (m_output.size() > maximumOutput) fail(QStringLiteral("The remote update returned too much output. Its outcome could not be confirmed; check the server."));
    });
    connect(process, &QProcess::readyReadStandardError, this, [this, process] {
        m_errorBytes += process->readAllStandardError().size();
        if (m_errorBytes > maximumOutput) fail(QStringLiteral("The remote update returned too much diagnostic output. Its outcome could not be confirmed; check the server."));
    });
    connect(process, &QProcess::errorOccurred, this, [this](QProcess::ProcessError error) {
        if (error == QProcess::FailedToStart) fail(QStringLiteral("OpenSSH could not be started. Install the SSH client and check your PATH."));
    });
    connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this, &RemoteUpdateService::finishCommand);
    m_deadline.start(qMax(1, m_options.commandTimeoutMs));
    process->start();
    // No input, credential upload, shell interpolation, or automatic retry.
    process->closeWriteChannel();
    emit changed();
}

void RemoteUpdateService::finishCommand(int exitCode, QProcess::ExitStatus exitStatus) {
    m_output += m_process->readAllStandardOutput();
    m_errorBytes += m_process->readAllStandardError().size();
    m_process->deleteLater(); m_process = nullptr;
    if (m_output.size() > maximumOutput || m_errorBytes > maximumOutput) {
        fail(QStringLiteral("The remote update response was too large. Check the server to confirm its outcome.")); return;
    }
    if (exitStatus != QProcess::NormalExit || exitCode != 0) {
        log(QStringLiteral("SSH update process exited with code %1%2.").arg(exitCode).arg(exitStatus == QProcess::CrashExit ? QStringLiteral(" (crashed)") : QString()));
        fail(exitCode == 127
            ? QStringLiteral("The remote headroom command was not found. Install the official CLI on the server and add it to the SSH account's PATH.")
            : exitCode == 255
            ? QStringLiteral("The SSH session failed. Check your key or agent, trusted host key, and connection. If the update had started, its outcome is unknown.")
            : QStringLiteral("The remote update did not complete. Run headroom update on the server for details. A usage-only restricted SSH key cannot run updates."));
        return;
    }
    m_targetVersion = acceptedVersion(m_output);
    m_output.fill('\0'); m_output.clear();
    if (m_targetVersion.isEmpty()) {
        fail(QStringLiteral("The remote command did not confirm a Headroom update. Check that the official CLI is installed and the SSH account permits headroom update; usage-only restricted keys cannot run it.")); return;
    }
    m_state = QStringLiteral("verifying");
    m_status = QStringLiteral("Remote update accepted. Waiting for the server to report %1…").arg(m_targetVersion);
    log(m_status); emit changed();
    // The CLI exits after committing to its detached transaction manager, before
    // the server restart finishes. Success requires the selected SSH server to
    // report the exact target version, not just an update command's exit code.
    m_deadline.start(qMax(1, m_options.verificationTimeoutMs));
    verifyServer();
}

void RemoteUpdateService::verifyServer() {
    if (m_state != "verifying" || m_reply) return;
    QUrl health = m_address;
    health.setPath(QStringLiteral("/api/v1/health"));
    auto reply = m_network.get(QNetworkRequest(health));
    m_reply = reply;
    connect(reply, &QNetworkReply::finished, this, [this, reply] {
        m_reply = nullptr;
        const auto health = QJsonDocument::fromJson(reply->readAll()).object();
        const QString status = health.value(QStringLiteral("status")).toString();
        const bool verified = reply->error() == QNetworkReply::NoError
            && reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt() == 200
            && health.value(QStringLiteral("version")).toString() == m_targetVersion
            && (status == "ok" || status == "degraded");
        reply->deleteLater();
        if (verified) {
            m_deadline.stop(); m_state = QStringLiteral("current");
            m_status = QStringLiteral("Remote server is running Headroom %1.").arg(m_targetVersion);
            log(m_status); emit changed(); emit completed();
        } else m_poll.start(qMax(1, m_options.pollIntervalMs));
    });
}

void RemoteUpdateService::stopWork() {
    m_deadline.stop(); m_poll.stop();
    if (m_reply) {
        auto reply = m_reply; m_reply = nullptr;
        reply->disconnect(this); reply->abort(); reply->deleteLater();
    }
    if (m_process) {
        auto process = m_process; m_process = nullptr;
        process->disconnect(this);
        if (process->state() != QProcess::NotRunning) { process->kill(); process->waitForFinished(1000); }
        process->deleteLater();
    }
    m_output.fill('\0'); m_output.clear();
}

void RemoteUpdateService::fail(const QString &message) {
    stopWork(); m_state = QStringLiteral("failed"); m_status = message;
    log(message); emit changed();
}

void RemoteUpdateService::log(const QString &message) const {
    if (m_options.diagnostic) m_options.diagnostic(QStringLiteral("remote-update: ") + message);
}
