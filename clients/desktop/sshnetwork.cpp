#include "sshnetwork.h"

#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QHostAddress>
#include <QProcess>
#include <QTimer>
#include <QRegularExpression>
#include <QStandardPaths>
#include <cmath>
#include <QSet>
#include <algorithm>

namespace {
constexpr qsizetype maximumFrameBytes = 2 * 1024 * 1024;
constexpr qsizetype maximumBodyBytes = 1024 * 1024;

bool containsUnsafe(const QString &value)
{
    return std::any_of(value.cbegin(), value.cend(), [](QChar c) {
        return c.isSpace() || c.unicode() < 0x20 || c.unicode() == 0x7f;
    });
}

bool allowed(const QByteArray &method, const QString &path)
{
    if (method == "GET") return path == QStringLiteral("/api/v1/usage") || path == QStringLiteral("/api/v1/health");
    if (method == "PUT") return path == QStringLiteral("/api/v1/providers/cursor/credentials")
        || path == QStringLiteral("/api/v1/providers/grok/credentials");
    // DO NOT test this endpoint or any code that could trigger a valuable banked reset.
    if (method == "POST") return path == QStringLiteral("/api/v1/providers/codex/reset");
    return false;
}

bool hasExactUniqueKeys(const QByteArray &json, const QSet<QString> &expected)
{
    QSet<QString> seen;
    int depth = 0;
    bool inString = false, escaped = false, expectingKey = false;
    qsizetype stringStart = -1;
    for (qsizetype i = 0; i < json.size(); ++i) {
        const char c = json.at(i);
        if (inString) {
            if (escaped) { escaped = false; continue; }
            if (c == '\\') { escaped = true; continue; }
            if (c != '"') continue;
            inString = false;
            if (depth == 1 && expectingKey) {
                const QByteArray literal = json.mid(stringStart, i - stringStart + 1);
                QJsonParseError error;
                const auto parsed = QJsonDocument::fromJson(QByteArrayLiteral("[") + literal + QByteArrayLiteral("]"), &error);
                if (error.error != QJsonParseError::NoError || !parsed.isArray() || !parsed.array().first().isString()) return false;
                const QString key = parsed.array().first().toString();
                if (seen.contains(key)) return false;
                seen.insert(key);
                expectingKey = false;
            }
            continue;
        }
        if (c == '"') { inString = true; stringStart = i; continue; }
        if (c == '{' || c == '[') { ++depth; if (depth == 1 && c == '{') expectingKey = true; continue; }
        if (c == '}' || c == ']') { --depth; continue; }
        if (depth == 1 && c == ',') expectingKey = true;
    }
    return !inString && depth == 0 && seen == expected;
}

class SshReply final : public QNetworkReply {
public:
    SshReply(QNetworkAccessManager::Operation operation, const QNetworkRequest &request,
             QIODevice *outgoingData, SshOptions options, QObject *parent)
        : QNetworkReply(parent), m_options(std::move(options))
    {
        setRequest(request);
        setUrl(request.url());
        setOperation(operation);
        open(QIODevice::ReadOnly | QIODevice::Unbuffered);

        const QByteArray method = operation == QNetworkAccessManager::GetOperation ? QByteArrayLiteral("GET")
            : operation == QNetworkAccessManager::PutOperation ? QByteArrayLiteral("PUT")
            : operation == QNetworkAccessManager::PostOperation ? QByteArrayLiteral("POST") : QByteArray();
        const QString path = request.url().path(QUrl::FullyDecoded);
        const bool resetRequest = method == QByteArrayLiteral("POST")
            && path == QStringLiteral("/api/v1/providers/codex/reset");
        QUrl address;
        if (!SshTransport::parseAddress(request.url().adjusted(QUrl::RemovePath).toString(QUrl::FullyEncoded), &address)
            || !allowed(method, path)) {
            QTimer::singleShot(0, this, [this] { fail(QNetworkReply::ProtocolInvalidOperationError); });
            return;
        }
        QByteArray body;
        if (outgoingData) body = outgoingData->read(maximumBodyBytes + 1);
        if ((method == "GET" && !body.isEmpty()) || body.size() > maximumBodyBytes) {
            body.fill('\0');
            QTimer::singleShot(0, this, [this] { fail(QNetworkReply::ContentOperationNotPermittedError); });
            return;
        }
        m_process = new QProcess(this);
        m_process->setProgram(m_options.executablePath.isEmpty() ? QStandardPaths::findExecutable(QStringLiteral("ssh")) : m_options.executablePath);
        m_process->setArguments(SshTransport::arguments(address));
        m_process->setProcessChannelMode(QProcess::SeparateChannels);
        connect(m_process, &QProcess::readyReadStandardOutput, this, [this] {
            m_stdout.append(m_process->readAllStandardOutput());
            if (m_stdout.size() > maximumFrameBytes) { m_invalid = true; m_process->kill(); }
        });
        connect(m_process, &QProcess::readyReadStandardError, this, [this] {
            m_stderrBytes += m_process->readAllStandardError().size();
            if (m_stderrBytes > maximumFrameBytes) { m_invalid = true; m_process->kill(); }
        });
        connect(m_process, &QProcess::errorOccurred, this, [this](QProcess::ProcessError error) {
            if (error == QProcess::FailedToStart && !isFinished()) fail(QNetworkReply::ConnectionRefusedError);
        });
        connect(m_process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
                [this](int code, QProcess::ExitStatus status) { complete(code, status); });
        m_deadline = new QTimer(this);
        m_deadline->setSingleShot(true);
        connect(m_deadline, &QTimer::timeout, this, [this] {
            m_timedOut = true;
            if (m_process && m_process->state() != QProcess::NotRunning) m_process->kill();
            else fail(QNetworkReply::TimeoutError);
        });

        const QJsonObject frame{{QStringLiteral("schema"), 1}, {QStringLiteral("method"), QString::fromLatin1(method)},
            {QStringLiteral("path"), path}, {QStringLiteral("body"), QString::fromLatin1(body.toBase64())}};
        m_frame = QJsonDocument(frame).toJson(QJsonDocument::Compact) + '\n';
        body.fill('\0');
        if (m_frame.size() > maximumFrameBytes) {
            m_frame.fill('\0');
            QTimer::singleShot(0, this, [this] { fail(QNetworkReply::ContentOperationNotPermittedError); });
            return;
        }
        QTimer::singleShot(0, this, [this, resetRequest] {
            if (isFinished() || m_aborted) { fail(QNetworkReply::OperationCanceledError); return; }
            if (m_process->program().isEmpty()) { fail(QNetworkReply::ConnectionRefusedError); return; }
            m_process->start();
            if (m_process->write(m_frame) != m_frame.size()) m_invalid = true;
            m_frame.fill('\0'); m_frame.clear();
            m_process->closeWriteChannel();
            m_deadline->start(resetRequest ? 90000 : qMax(1, m_options.timeoutMs));
        });
    }

    ~SshReply() override
    {
        if (m_deadline) m_deadline->stop();
        if (m_process) {
            disconnect(m_process, nullptr, this, nullptr);
            if (m_process->state() != QProcess::NotRunning) {
                m_process->kill();
                m_process->waitForFinished(2000);
            }
        }
        m_stdout.fill('\0'); m_data.fill('\0'); m_frame.fill('\0');
    }

    void abort() override
    {
        if (isFinished()) return;
        m_aborted = true;
        if (m_process && m_process->state() != QProcess::NotRunning) m_process->kill();
        else fail(QNetworkReply::OperationCanceledError);
    }
    qint64 bytesAvailable() const override { return m_data.size() - m_offset + QNetworkReply::bytesAvailable(); }

protected:
    qint64 readData(char *data, qint64 maxSize) override
    {
        const qint64 count = qMin(maxSize, qint64(m_data.size() - m_offset));
        if (count <= 0) return isFinished() ? -1 : 0;
        memcpy(data, m_data.constData() + m_offset, size_t(count));
        m_offset += count;
        return count;
    }

private:
    void fail(QNetworkReply::NetworkError error)
    {
        if (isFinished()) return;
        if (m_deadline) m_deadline->stop();
        setError(error, QStringLiteral("SSH connection failed."));
        setFinished(true);
        emit errorOccurred(error);
        emit finished();
    }
    void complete(int code, QProcess::ExitStatus status)
    {
        if (isFinished()) return;
        m_deadline->stop();
        m_stdout.append(m_process->readAllStandardOutput());
        m_stderrBytes += m_process->readAllStandardError().size();
        if (m_aborted) { fail(QNetworkReply::OperationCanceledError); return; }
        if (m_timedOut) { fail(QNetworkReply::TimeoutError); return; }
        if (m_invalid || code != 0 || status != QProcess::NormalExit || m_stdout.isEmpty()
            || m_stdout.size() > maximumFrameBytes || !m_stdout.endsWith('\n') || m_stdout.count('\n') != 1) {
            fail(QNetworkReply::RemoteHostClosedError); return;
        }
        const QByteArray line = m_stdout.left(m_stdout.size() - 1);
        QJsonParseError parseError;
        const QJsonDocument document = QJsonDocument::fromJson(line, &parseError);
        const QJsonObject object = document.object();
        const auto keys = object.keys();
        const double schema = object.value(QStringLiteral("schema")).toDouble(-1);
        const double statusValue = object.value(QStringLiteral("status")).toDouble(-1);
        if (parseError.error != QJsonParseError::NoError || !document.isObject()
            || !hasExactUniqueKeys(line, QSet<QString>{QStringLiteral("schema"), QStringLiteral("status"), QStringLiteral("body")})
            || keys != QStringList({QStringLiteral("body"), QStringLiteral("schema"), QStringLiteral("status")})
            || !object.value(QStringLiteral("schema")).isDouble() || schema != 1.0
            || !object.value(QStringLiteral("status")).isDouble() || statusValue != std::floor(statusValue)
            || statusValue < 100 || statusValue > 599 || m_stderrBytes > maximumFrameBytes
            || !object.value(QStringLiteral("body")).isString()) {
            fail(QNetworkReply::ProtocolFailure); return;
        }
        const QByteArray encoded = object.value(QStringLiteral("body")).toString().toLatin1();
        const QByteArray decoded = QByteArray::fromBase64(encoded, QByteArray::AbortOnBase64DecodingErrors);
        if (decoded.size() > maximumBodyBytes || decoded.toBase64() != encoded) {
            fail(QNetworkReply::ProtocolFailure); return;
        }
        m_data = decoded;
        m_stdout.fill('\0'); m_stdout.clear();
        setAttribute(QNetworkRequest::HttpStatusCodeAttribute, object.value(QStringLiteral("status")).toInt());
        setHeader(QNetworkRequest::ContentLengthHeader, m_data.size());
        setFinished(true);
        if (!m_data.isEmpty()) emit readyRead();
        emit finished();
    }

    SshOptions m_options;
    QProcess *m_process = nullptr;
    QTimer *m_deadline = nullptr;
    QByteArray m_stdout, m_data, m_frame;
    qsizetype m_offset = 0, m_stderrBytes = 0;
    bool m_invalid = false, m_timedOut = false, m_aborted = false;
};
}

bool SshTransport::parseAddress(const QString &address, QUrl *normalized, QString *error)
{
    auto reject = [&](const QString &message) { if (error) *error = message; return false; };
    if (address.isEmpty() || address != address.trimmed() || containsUnsafe(address))
        return reject(QStringLiteral("Use an SSH address without spaces or control characters."));
    const QUrl url(address, QUrl::StrictMode);
    if (!url.isValid() || url.scheme() != QStringLiteral("ssh") || url.host().isEmpty()
        || url.userInfo().contains(':') || url.hasQuery() || url.hasFragment()
        || !url.path().isEmpty() || url.port(-1) == 0)
        return reject(QStringLiteral("Use ssh://[user@]host[:port] without a password, path, query, or fragment."));
    static const QRegularExpression safeHost(QStringLiteral("^[A-Za-z0-9_][A-Za-z0-9._-]*$"));
    static const QRegularExpression safeUser(QStringLiteral("^[A-Za-z0-9_][A-Za-z0-9._-]*$"));
    QHostAddress numericAddress;
    if ((!safeHost.match(url.host()).hasMatch() && !numericAddress.setAddress(url.host()))
        || (!url.userName().isEmpty() && !safeUser.match(url.userName()).hasMatch()))
        return reject(QStringLiteral("The SSH address contains an invalid host or user."));
    QUrl clean;
    clean.setScheme(QStringLiteral("ssh")); clean.setHost(url.host());
    if (!url.userName().isEmpty()) clean.setUserName(url.userName());
    if (url.port(-1) > 0) clean.setPort(url.port());
    if (normalized) *normalized = clean;
    if (error) error->clear();
    return true;
}

QStringList SshTransport::arguments(const QUrl &address)
{
    QStringList args{QStringLiteral("-T"), QStringLiteral("-o"), QStringLiteral("BatchMode=yes"),
        QStringLiteral("-o"), QStringLiteral("StrictHostKeyChecking=yes"), QStringLiteral("-o"), QStringLiteral("PermitLocalCommand=no"),
        QStringLiteral("-o"), QStringLiteral("ControlMaster=no"), QStringLiteral("-o"), QStringLiteral("ControlPath=none"),
        QStringLiteral("-o"), QStringLiteral("ClearAllForwardings=yes"), QStringLiteral("-o"), QStringLiteral("ForwardAgent=no"),
        QStringLiteral("-o"), QStringLiteral("ForwardX11=no"), QStringLiteral("-o"), QStringLiteral("RemoteCommand=none"),
        QStringLiteral("-o"), QStringLiteral("RequestTTY=no"), QStringLiteral("-o"), QStringLiteral("ConnectTimeout=8"),
        QStringLiteral("-o"), QStringLiteral("ConnectionAttempts=1")};
    if (!address.userName().isEmpty()) args << QStringLiteral("-l") << address.userName();
    if (address.port(-1) > 0) args << QStringLiteral("-p") << QString::number(address.port());
    args << QStringLiteral("--") << address.host() << QStringLiteral("usage-server --ssh-stdio");
    return args;
}

QStringList SshTransport::updateArguments(const QUrl &address)
{
    auto args = arguments(address);
    // This is a fixed command, never assembled from UI text. Native Windows/WSL
    // pairing remains a separate, explicit action; this updates only this host.
    args.last() = QStringLiteral("headroom update --this-install-only");
    return args;
}

SshNetworkAccessManager::SshNetworkAccessManager(SshOptions options, QObject *parent)
    : QNetworkAccessManager(parent), m_options(std::move(options)) {}

void SshNetworkAccessManager::setOptions(SshOptions options) { m_options = std::move(options); }

QNetworkReply *SshNetworkAccessManager::createRequest(Operation operation, const QNetworkRequest &request, QIODevice *outgoingData)
{
    return new SshReply(operation, request, outgoingData, m_options, this);
}
