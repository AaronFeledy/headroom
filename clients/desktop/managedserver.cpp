#include "managedserver.h"
#include "statemachine.h"

#include <QCoreApplication>
#include <QDir>
#include <QFileInfo>
#include <QHostAddress>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkReply>
#include <QNetworkProxy>
#include <QNetworkRequest>
#include <QProcessEnvironment>
#include <QRandomGenerator>
#include <QSet>
#include <QSignalBlocker>
#include <cctype>
#include <utility>
#ifdef Q_OS_LINUX
#include <signal.h>
#include <sys/prctl.h>
#include <unistd.h>
#endif

namespace {
constexpr qsizetype maximumHealthBytes = 1024 * 1024;
constexpr qsizetype maximumIdentityBytes = 8 * 1024;

QByteArray randomHex(int words)
{
    QByteArray result;
    result.reserve(words * 16);
    auto generator = QRandomGenerator::system();
    for (int i = 0; i < words; ++i)
        result += QByteArray::number(generator->generate64(), 16).rightJustified(16, '0');
    return result;
}

bool exactTopLevelKeys(const QByteArray &json, const QSet<QString> &expected)
{
    QSet<QString> found;
    int objectDepth = 0;
    int arrayDepth = 0;
    for (qsizetype index = 0; index < json.size(); ++index) {
        const char character = json.at(index);
        if (character == '{') { ++objectDepth; continue; }
        if (character == '}') { --objectDepth; continue; }
        if (character == '[') { ++arrayDepth; continue; }
        if (character == ']') { --arrayDepth; continue; }
        if (character != '"') continue;
        const qsizetype start = index++;
        bool escaped = false;
        while (index < json.size()) {
            const char current = json.at(index);
            if (!escaped && current == '"') break;
            if (!escaped && current == '\\') escaped = true;
            else escaped = false;
            ++index;
        }
        if (index >= json.size()) return false;
        qsizetype next = index + 1;
        while (next < json.size() && std::isspace(static_cast<unsigned char>(json.at(next)))) ++next;
        if (objectDepth != 1 || arrayDepth != 0 || next >= json.size() || json.at(next) != ':') continue;
        const QByteArray encoded = '[' + json.mid(start, index - start + 1) + ']';
        const auto decoded = QJsonDocument::fromJson(encoded).array();
        if (decoded.size() != 1 || !decoded.first().isString()) return false;
        const QString key = decoded.first().toString();
        if (found.contains(key)) return false;
        found.insert(key);
    }
    return found == expected;
}

bool validLocalEndpoint(const QUrl &url)
{
    const QHostAddress address(url.host());
    return url.isValid() && url.scheme() == QStringLiteral("http")
        && address.isLoopback() && url.port(7823) > 0
        && url.userInfo().isEmpty() && !url.hasQuery() && !url.hasFragment();
}

bool compatibleHealth(const QByteArray &body)
{
    QJsonParseError error;
    const auto document = QJsonDocument::fromJson(body, &error);
    if (error.error != QJsonParseError::NoError || !document.isObject()) return false;
    const auto object = document.object();
    const QString status = object.value(QStringLiteral("status")).toString();
    if (status != QStringLiteral("ok") && status != QStringLiteral("degraded")) return false;
    if (!object.value(QStringLiteral("version")).isString()
        || object.value(QStringLiteral("version")).toString().trimmed().isEmpty()
        || !object.value(QStringLiteral("providers")).isArray()) return false;
    for (const auto &item : object.value(QStringLiteral("providers")).toArray()) {
        if (!item.isObject()) return false;
        const auto provider = item.toObject();
        if (!provider.value(QStringLiteral("name")).isString()
            || provider.value(QStringLiteral("name")).toString().isEmpty()
            || !provider.value(QStringLiteral("ok")).isBool()
            || !(provider.value(QStringLiteral("fetched_at")).isString()
                 || provider.value(QStringLiteral("fetched_at")).isNull())) return false;
    }
    return true;
}
}

ManagedServer::ManagedServer(ManagedServerOptions options, QObject *parent)
    : QObject(parent), m_options(std::move(options))
{
    m_network.setProxy(QNetworkProxy::NoProxy);
    m_readinessTimer.setSingleShot(true);
    m_restartTimer.setSingleShot(true);
    m_identityTimer.setSingleShot(true);
    connect(&m_readinessTimer, &QTimer::timeout, this, [this] { probe(ProbePurpose::Readiness); });
    connect(&m_restartTimer, &QTimer::timeout, this, [this] { probe(ProbePurpose::Initial); });
    connect(&m_identityTimer, &QTimer::timeout, this, [this] {
        failOwnedStartup(QStringLiteral("The bundled usage server did not establish a private connection in time."),
                         QStringLiteral("identity"));
    });
}

ServerConnection ManagedServer::connection() const
{
    if (!m_sessionCertificate.isNull()) return {m_sessionUrl, m_sessionToken, m_sessionCertificate};
    return {m_options.localUrl, QByteArray(), QSslCertificate()};
}

ManagedServer::~ManagedServer()
{
    m_ensureAfterRetire = false;
    ++m_generation;
    cancelAsync();
    auto process = m_process;
    m_process = nullptr;
    if (process) {
        process->disconnect(this);
        if (process->state() != QProcess::NotRunning) process->kill();
        closeWindowsJob();
        if (process->state() != QProcess::NotRunning) process->waitForFinished(1000);
        delete process;
    } else closeWindowsJob();
    auto retiring = m_retiringProcess;
    m_retiringProcess = nullptr;
    if (retiring) {
        retiring->disconnect(this);
        if (retiring->state() != QProcess::NotRunning) retiring->kill();
        if (retiring->state() != QProcess::NotRunning) retiring->waitForFinished(1000);
        delete retiring;
    }
    // Every other teardown path scrubs the session material; the destructor must too.
    // Signals stay blocked: connectionChanged() here would reach slots whose objects
    // are already destroyed.
    const QSignalBlocker blocker(this);
    clearSession();
}

void ManagedServer::configure(const QString &mode, const QString &token)
{
    const QString normalized = mode == QStringLiteral("local") ? QStringLiteral("local") : QStringLiteral("remote");
    if (normalized == m_mode && token == m_token) return;
    ++m_generation;
    cancelAsync();
    m_ensureAfterRetire = normalized == QStringLiteral("local");
    disposeProcess(true);
    m_mode = normalized;
    m_token = token;
    m_readinessAttempt = 0;
    m_restartCount = 0;
    enter(m_mode == QStringLiteral("local") ? State::Idle : State::Remote);
}

void ManagedServer::ensureAvailable()
{
    if (m_mode != QStringLiteral("local")) return;
    if (isAvailable()) {
        emit available();
        return;
    }
    if (m_retiringProcess) {
        m_ensureAfterRetire = true;
        return;
    }
    if (m_probe || m_readinessTimer.isActive() || m_restartTimer.isActive() || m_identityTimer.isActive()
        || (m_process && m_process->state() != QProcess::NotRunning)) return;
    if (m_state == State::Failed) m_restartCount = 0;
    m_readinessAttempt = 0;
    enter(State::Probing);
    probe(ProbePurpose::Initial);
}

void ManagedServer::reportConnectionFailure()
{
    if (m_mode != QStringLiteral("local") || !isAvailable()) return;
    cancelAsync();
    enter(State::Probing);
    if (m_process && m_process->state() != QProcess::NotRunning) {
        beginReadiness();
    } else {
        // A dead child must not carry its ownership or private session into discovery.
        disposeProcess(false);
        clearSession();
        probe(ProbePurpose::Initial);
    }
}

void ManagedServer::stopOwned()
{
    ++m_generation;
    cancelAsync();
    m_ensureAfterRetire = false;
    disposeProcess(true);
    enter(m_mode == QStringLiteral("local") ? State::Idle : State::Remote, m_message);
}

void ManagedServer::cancelAsync()
{
    m_readinessTimer.stop();
    m_restartTimer.stop();
    m_identityTimer.stop();
    if (m_probe) {
        auto reply = m_probe;
        m_probe.clear();
        reply->disconnect(this);
        reply->abort();
        reply->deleteLater();
    }
}

void ManagedServer::probe(ProbePurpose purpose)
{
    if (m_mode != QStringLiteral("local") || m_probe) return;
    if (purpose == ProbePurpose::Readiness && m_sessionCertificate.isNull()) {
        failOwnedStartup(QStringLiteral("The bundled usage server did not establish a verified private connection."),
                         QStringLiteral("identity"));
        return;
    }
    if (!validLocalEndpoint(m_options.localUrl)) {
        handleProbe(purpose, ProbeResult::NetworkFailure);
        return;
    }
    const int timeoutMs = purpose == ProbePurpose::Initial
        ? m_options.probeTimeoutMs : m_options.readinessProbeTimeoutMs;
    QUrl endpoint = m_options.localUrl.resolved(QUrl(QStringLiteral("api/v1/health")));
    QNetworkRequest request(endpoint);
    request.setRawHeader("Accept", "application/json");
    request.setRawHeader("User-Agent", "Headroom/" HEADROOM_VERSION);
    request.setAttribute(QNetworkRequest::RedirectPolicyAttribute, QNetworkRequest::ManualRedirectPolicy);
    request.setTransferTimeout(timeoutMs);
    const ServerConnection transport = connection();
    if (purpose == ProbePurpose::Readiness && transport.isPinned()) {
        request.setUrl(transport.url.resolved(QUrl(QStringLiteral("api/v1/health"))));
        request.setRawHeader("Authorization", "Bearer " + transport.token);
        ServerTransport::secureRequest(request, transport.certificate);
    }
    const quint64 generation = m_generation;
    auto reply = m_network.get(request);
    if (purpose == ProbePurpose::Readiness && transport.isPinned())
        ServerTransport::requirePinnedPeer(reply, transport.certificate);
    if (m_options.probeObserver) m_options.probeObserver(reply);
    m_probe = reply;
    reply->setReadBufferSize(maximumHealthBytes + 1);
    auto timeout = new QTimer(reply);
    timeout->setSingleShot(true);
    connect(timeout, &QTimer::timeout, reply, [reply] {
        reply->setProperty("headroomTimedOut", true);
        if (!reply->isFinished()) reply->abort();
    });
    timeout->start(timeoutMs);
    connect(reply, &QIODevice::readyRead, reply, [reply] {
        if (reply->bytesAvailable() > maximumHealthBytes) reply->abort();
    });
    connect(reply, &QNetworkReply::finished, this, [this, reply, generation, purpose] {
        if (m_probe == reply) m_probe.clear();
        const int status = reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const auto error = reply->error();
        const QByteArray body = reply->isOpen() ? reply->readAll() : QByteArray();
        ProbeResult result = ProbeResult::NetworkFailure;
        if (reply->property("headroomTimedOut").toBool()) result = ProbeResult::TimedOut;
        else if (status == 401 || status == 403) result = ProbeResult::AuthRejected;
        else if (status >= 300 && status < 400) result = ProbeResult::Redirected;
        else if (error == QNetworkReply::ConnectionRefusedError && status == 0) result = ProbeResult::Refused;
        else if (error == QNetworkReply::NoError && status == 200
                 && body.size() <= maximumHealthBytes && compatibleHealth(body)) result = ProbeResult::Compatible;
        else if (error == QNetworkReply::NoError && status == 200) result = ProbeResult::Malformed;
        reply->deleteLater();
        if (generation != m_generation || m_mode != QStringLiteral("local")) return;
        handleProbe(purpose, result);
    });
}

void ManagedServer::handleProbe(ProbePurpose purpose, ProbeResult result)
{
    if (result == ProbeResult::Compatible) {
        enter(purpose == ProbePurpose::Initial ? State::Attached : State::Started);
        emit available();
        return;
    }
    if (purpose == ProbePurpose::Initial && result == ProbeResult::Refused) {
        spawn();
        return;
    }
    if (purpose == ProbePurpose::Readiness
        && (result == ProbeResult::Refused || result == ProbeResult::TimedOut || result == ProbeResult::NetworkFailure)
        && ++m_readinessAttempt < m_options.readinessAttempts) {
        m_readinessTimer.start(m_options.readinessIntervalMs);
        return;
    }
    if (purpose == ProbePurpose::Readiness) {
        m_ensureAfterRetire = false;
        disposeProcess(true);
    }
    switch (result) {
    case ProbeResult::AuthRejected:
        setFailure(purpose == ProbePurpose::Initial
            ? QStringLiteral("The local server requires explicit configuration. Add it as a Remote connection.")
            : QStringLiteral("The bundled usage server rejected its private session token."), QStringLiteral("auth")); break;
    case ProbeResult::Redirected:
    case ProbeResult::Malformed:
        setFailure(QStringLiteral("Port 7823 is occupied by an incompatible local service."), QStringLiteral("local-service")); break;
    case ProbeResult::TimedOut:
        setFailure(purpose == ProbePurpose::Initial
            ? QStringLiteral("The local server health check timed out. No local server was started.")
            : QStringLiteral("The bundled local server did not answer its health check in time."), QStringLiteral("timeout")); break;
    case ProbeResult::NetworkFailure:
        setFailure(QStringLiteral("The local endpoint could not be verified. No local server was started."), QStringLiteral("network")); break;
    case ProbeResult::Refused:
        setFailure(QStringLiteral("The bundled local server did not become ready in time."), QStringLiteral("readiness")); break;
    case ProbeResult::Compatible: break;
    }
}

QString ManagedServer::binaryPath() const
{
    if (!m_options.executablePath.isEmpty()) return QFileInfo(m_options.executablePath).absoluteFilePath();
#ifdef Q_OS_WIN
    return QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("usage-server.exe"));
#else
    return QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("usage-server"));
#endif
}

void ManagedServer::spawn()
{
    const QString path = binaryPath();
    const QFileInfo binary(path);
    if (!binary.isFile() || !binary.isExecutable()) {
        setFailure(QStringLiteral("The bundled usage server is missing. Reinstall or repair Headroom."), QStringLiteral("binary"));
        return;
    }
    m_ensureAfterRetire = false;
    disposeProcess(true);
    clearSession();
    auto process = new QProcess(this);
    m_process = process;
    process->setProgram(path);
    process->setArguments({QStringLiteral("--listen-addr"),
        QStringLiteral("127.0.0.1:%1").arg(m_options.localUrl.port(7823)), QStringLiteral("--desktop-session"),
        QStringLiteral("--ssh-access=false")});
    auto environment = QProcessEnvironment::systemEnvironment();
    environment.remove(QStringLiteral("USAGE_AUTH_TOKEN"));
    process->setProcessEnvironment(environment);
    process->setProcessChannelMode(QProcess::SeparateChannels);
    process->setStandardErrorFile(QProcess::nullDevice());
#ifdef Q_OS_LINUX
    const pid_t creatingProcess = getpid();
    process->setChildProcessModifier([creatingProcess] {
        if (prctl(PR_SET_PDEATHSIG, SIGTERM) == -1 || getppid() != creatingProcess) _exit(127);
    });
#endif
    const quint64 generation = m_generation;
    connect(process, &QProcess::readyReadStandardOutput, this, [this, process, generation] {
        if (generation == m_generation && process == m_process) collectIdentity();
    });
    connect(process, &QProcess::started, this, [this, process, generation] {
        if (generation != m_generation || process != m_process) return;
        if (!assignWindowsJob()) {
            m_ensureAfterRetire = false;
            disposeProcess(true);
            setFailure(QStringLiteral("Headroom could not safely own the local server process."), QStringLiteral("ownership"));
            return;
        }
        enter(State::Starting, m_message);
        m_sessionNonce = randomHex(2);
        m_sessionToken = randomHex(4);
        const QByteArray frame = QJsonDocument(QJsonObject{
            {QStringLiteral("schema"), 1},
            {QStringLiteral("nonce"), QString::fromLatin1(m_sessionNonce)},
            {QStringLiteral("token"), QString::fromLatin1(m_sessionToken)},
        }).toJson(QJsonDocument::Compact) + '\n';
        if (process->write(frame) != frame.size()) {
            failOwnedStartup(QStringLiteral("Headroom could not initialize the bundled server's private connection."),
                             QStringLiteral("identity"));
            return;
        }
        process->closeWriteChannel();
        m_identityTimer.start(m_options.sessionHandshakeTimeoutMs);
    });
    connect(process, &QProcess::errorOccurred, this, [this, process, generation](QProcess::ProcessError error) {
        if (generation != m_generation || process != m_process || error != QProcess::FailedToStart) return;
        disposeProcess(false);
        setFailure(QStringLiteral("The bundled usage server could not be started."), QStringLiteral("launch"));
    });
    connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
            [this, process, generation](int, QProcess::ExitStatus) {
        if (generation != m_generation || process != m_process) return;
        cancelAsync();
        ++m_generation;
        closeWindowsJob();
        m_process = nullptr;
        process->deleteLater();
        m_readinessTimer.stop();
        if (m_mode != QStringLiteral("local")) return;
        if (m_restartCount >= m_options.restartLimit) {
            setFailure(QStringLiteral("The local server stopped repeatedly. Use Refresh to retry."), QStringLiteral("restart"));
            return;
        }
        ++m_restartCount;
        enter(State::Restarting, m_message);
        m_restartTimer.start(qMin(1000, 100 * m_restartCount));
    });
    enter(State::Starting, m_message);
    process->start();
}

void ManagedServer::collectIdentity()
{
    if (!m_process) return;
    const QByteArray incoming = m_process->readAllStandardOutput();
    if (!m_sessionCertificate.isNull()) {
        if (!incoming.isEmpty())
            failOwnedStartup(QStringLiteral("The bundled usage server wrote unexpected private-session output."),
                             QStringLiteral("identity"));
        return;
    }
    m_identityOutput += incoming;
    if (m_identityOutput.size() > maximumIdentityBytes)
        failOwnedStartup(QStringLiteral("The bundled usage server returned an invalid private connection identity."),
                         QStringLiteral("identity"));
    else if (m_identityOutput.contains('\n'))
        finishIdentity();
}

void ManagedServer::finishIdentity()
{
    if (!m_process || m_sessionNonce.isEmpty() || m_sessionToken.isEmpty()) return;
    m_identityTimer.stop();
    const QByteArray frame = m_identityOutput;
    const bool framed = !frame.isEmpty() && frame.size() <= maximumIdentityBytes && frame.endsWith('\n')
        && frame.count('\n') == 1 && !frame.contains("PRIVATE KEY") && !frame.contains(m_sessionToken);
    QJsonParseError parseError;
    const auto document = framed ? QJsonDocument::fromJson(frame.left(frame.size() - 1), &parseError) : QJsonDocument();
    const auto object = document.object();
    const QSet<QString> expectedKeys{QStringLiteral("schema"), QStringLiteral("nonce"),
        QStringLiteral("address"), QStringLiteral("certificate")};
    QSet<QString> actualKeys;
    for (const auto &key : object.keys()) actualKeys.insert(key);
    const QString address = object.value(QStringLiteral("address")).toString();
    const int colon = address.lastIndexOf(':');
    QHostAddress host;
    bool portOk = false;
    const quint16 port = colon >= 0 ? address.mid(colon + 1).toUShort(&portOk) : 0;
    QString hostText = colon >= 0 ? address.left(colon) : QString();
    if (hostText.startsWith('[') && hostText.endsWith(']')) hostText = hostText.mid(1, hostText.size() - 2);
    const bool addressOk = host.setAddress(hostText) && host.isLoopback() && portOk && port > 0
        && host == QHostAddress(m_options.localUrl.host()) && port == m_options.localUrl.port(7823);
    const QByteArray certificatePEM = object.value(QStringLiteral("certificate")).toString().toUtf8();
    const QList<QSslCertificate> certificates = QSslCertificate::fromData(certificatePEM, QSsl::Pem);
    if (!framed || parseError.error != QJsonParseError::NoError || !document.isObject() ||
        !exactTopLevelKeys(frame.left(frame.size() - 1), expectedKeys) || actualKeys != expectedKeys ||
        !object.value(QStringLiteral("schema")).isDouble() ||
        object.value(QStringLiteral("schema")).toDouble() != 1.0 ||
        !object.value(QStringLiteral("nonce")).isString() ||
        object.value(QStringLiteral("nonce")).toString().toLatin1() != m_sessionNonce ||
        !object.value(QStringLiteral("address")).isString() || !addressOk ||
        !object.value(QStringLiteral("certificate")).isString() || certificates.size() != 1 ||
        certificates.first().isNull() || !certificates.first().isSelfSigned() ||
        certificatePEM.trimmed() != certificates.first().toPem().trimmed()) {
        failOwnedStartup(QStringLiteral("The bundled usage server returned an invalid private connection identity."),
                         QStringLiteral("identity"));
        return;
    }
    m_identityOutput.fill('\0');
    m_identityOutput.clear();
    m_sessionCertificate = certificates.first();
    m_sessionUrl = QUrl(QStringLiteral("https://") + address + '/');
    m_network.clearConnectionCache();
    emit connectionChanged();
    beginReadiness();
}

void ManagedServer::clearSession()
{
    const bool changed = !m_sessionUrl.isEmpty() || !m_sessionToken.isEmpty() || !m_sessionCertificate.isNull();
    m_identityOutput.fill('\0');
    m_identityOutput.clear();
    m_sessionNonce.fill('\0');
    m_sessionNonce.clear();
    m_sessionToken.fill('\0');
    m_sessionToken.clear();
    m_sessionCertificate.clear();
    m_sessionUrl.clear();
    m_network.clearConnectionCache();
    if (changed) emit connectionChanged();
}

void ManagedServer::failOwnedStartup(const QString &message, const QString &kind)
{
    if (!m_process) return;
    ++m_generation;
    cancelAsync();
    m_ensureAfterRetire = false;
    disposeProcess(true);
    setFailure(message, kind);
}

void ManagedServer::beginReadiness()
{
    m_readinessAttempt = 0;
    probe(ProbePurpose::Readiness);
}

QString ManagedServer::stateName(State state)
{
    switch (state) {
    case State::Remote: return QStringLiteral("remote");
    case State::Idle: return QStringLiteral("idle");
    case State::Probing: return QStringLiteral("probing");
    case State::Starting: return QStringLiteral("starting");
    case State::Restarting: return QStringLiteral("restarting");
    case State::Attached: return QStringLiteral("attached");
    case State::Started: return QStringLiteral("started");
    case State::Failed: return QStringLiteral("failed");
    }
    Q_UNREACHABLE();
}

bool ManagedServer::allowed(State from, State to)
{
    if (to == State::Idle || to == State::Remote) return true;
    switch (from) {
    case State::Remote:
        return false;
    case State::Idle:
        return to == State::Probing;
    case State::Probing:
        return to == State::Attached || to == State::Starting || to == State::Started
            || to == State::Restarting || to == State::Failed;
    case State::Starting:
        return to == State::Starting || to == State::Started || to == State::Restarting || to == State::Failed;
    case State::Restarting:
        return to == State::Attached || to == State::Starting || to == State::Failed;
    case State::Attached:
        return to == State::Probing;
    case State::Started:
        return to == State::Probing || to == State::Restarting || to == State::Failed;
    case State::Failed:
        return to == State::Probing;
    }
    Q_UNREACHABLE();
}

void ManagedServer::enter(State next, const QString &message)
{
    if (!allowed(m_state, next)
        || (next == State::Started && !m_process)
        || (next == State::Attached && m_process))
        reportStateMachineViolation("ManagedServer", stateName(m_state), stateName(next));
    m_state = next;
    m_message = message;
    // Publish session loss only after availability reflects the new lifecycle state.
    if (!m_process) clearSession();
    emit stateChanged();
}

void ManagedServer::setFailure(const QString &message, const QString &kind)
{
    enter(State::Failed, message);
    emit unavailable(message, kind);
}

void ManagedServer::disposeProcess(bool kill)
{
    auto process = m_process;
    m_process = nullptr;
    if (!process) {
        closeWindowsJob();
        return;
    }
    process->disconnect(this);
    if (kill && process->state() != QProcess::NotRunning) process->kill();
    closeWindowsJob();
    if (process->state() == QProcess::NotRunning) process->deleteLater();
    else {
        m_retiringProcess = process;
        connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
                [this, process](int, QProcess::ExitStatus) {
            if (m_retiringProcess == process) m_retiringProcess.clear();
            process->deleteLater();
            if (m_ensureAfterRetire && m_mode == QStringLiteral("local")) {
                m_ensureAfterRetire = false;
                QTimer::singleShot(0, this, &ManagedServer::ensureAvailable);
            }
        });
    }
}
