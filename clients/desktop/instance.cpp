#include "instance.h"

#include <QCryptographicHash>
#include <QDir>
#include <QFileDevice>
#include <QFileInfo>
#include <QElapsedTimer>
#include <QLocalServer>
#include <QLocalSocket>
#include <QLockFile>
#include <QStandardPaths>
#include <QTimer>
#include <QPointer>

#ifndef Q_OS_WIN
#include <cerrno>
#include <fcntl.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>
#endif

namespace {
#ifndef Q_OS_WIN
class ScopedFd {
public:
    explicit ScopedFd(int fd = -1) : m_fd(fd) {}
    ~ScopedFd() { if (m_fd >= 0) ::close(m_fd); }
    ScopedFd(const ScopedFd &) = delete;
    ScopedFd &operator=(const ScopedFd &) = delete;
    int get() const { return m_fd; }
    void reset(int fd)
    {
        if (m_fd >= 0) ::close(m_fd);
        m_fd = fd;
    }
private:
    int m_fd;
};

bool safeAncestor(const struct stat &status, uid_t systemUid)
{
    return S_ISDIR(status.st_mode) &&
           (status.st_uid == geteuid() || status.st_uid == systemUid) &&
           (!(status.st_mode & (S_IWGRP | S_IWOTH)) || (status.st_mode & S_ISVTX));
}

bool privateDirectory(const struct stat &status)
{
    return S_ISDIR(status.st_mode) && status.st_uid == geteuid() &&
           (status.st_mode & 0777) == 0700;
}

bool openSafeDirectory(const QString &path, bool requirePrivateFinal, ScopedFd &result)
{
    if (path.isEmpty() || !QDir::isAbsolutePath(path)) return false;

    ScopedFd current(::open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC));
    if (current.get() < 0) return false;
    struct stat rootStatus {};
    if (::fstat(current.get(), &rootStatus) != 0 || !S_ISDIR(rootStatus.st_mode)) return false;
    const uid_t systemUid = rootStatus.st_uid;
    const QStringList components = QDir::cleanPath(path).split('/', Qt::SkipEmptyParts);
    for (qsizetype index = 0; index < components.size(); ++index) {
        const QByteArray component = QFile::encodeName(components.at(index));
        const int next = ::openat(current.get(), component.constData(),
                                  O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
        if (next < 0) return false;
        current.reset(next);
        struct stat status {};
        if (::fstat(current.get(), &status) != 0 ||
            (index + 1 == components.size() && requirePrivateFinal
                 ? !privateDirectory(status) : !safeAncestor(status, systemUid))) {
            return false;
        }
    }
    result.reset(::fcntl(current.get(), F_DUPFD_CLOEXEC, 0));
    return result.get() >= 0;
}

bool openOrCreatePrivateDirectory(int parent, const QByteArray &name, ScopedFd &result)
{
    bool created = false;
    if (::mkdirat(parent, name.constData(), 0700) == 0) {
        created = true;
    } else if (errno != EEXIST) {
        return false;
    }
    const int fd = ::openat(parent, name.constData(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    if (fd < 0) return false;
    result.reset(fd);
    if (created && ::fchmod(result.get(), 0700) != 0) return false;
    struct stat status {};
    return ::fstat(result.get(), &status) == 0 && privateDirectory(status);
}

bool safeEndpoint(const QString &path, mode_t expectedType)
{
    const QByteArray nativePath = QFile::encodeName(path);
    struct stat status {};
    if (::lstat(nativePath.constData(), &status) != 0) return errno == ENOENT;
    return (status.st_mode & S_IFMT) == expectedType && status.st_uid == geteuid();
}
#endif
}

InstanceService::InstanceService(QString configPath, QObject *parent) : QObject(parent)
{
    QFileInfo info(configPath);
    const QFileInfo parentInfo(info.absolutePath());
    QString canonical = info.exists() ? info.canonicalFilePath()
                                      : QDir(parentInfo.exists() ? parentInfo.canonicalFilePath() : parentInfo.absoluteFilePath())
                                            .filePath(info.fileName());
    QString userRoot = QDir::cleanPath(QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation));
#ifdef Q_OS_WIN
    canonical = canonical.toCaseFolded();
    userRoot = userRoot.toCaseFolded();
#endif
    const QByteArray identity = userRoot.toUtf8() + '\0' + QDir::cleanPath(canonical).toUtf8();
    const QString digest = QString::fromLatin1(QCryptographicHash::hash(identity, QCryptographicHash::Sha256).toHex().left(24));
#ifdef Q_OS_WIN
    const QString lockDirectory = QDir(QStandardPaths::writableLocation(QStandardPaths::TempLocation))
                                      .filePath("Headroom/" + digest);
    if (!QDir().mkpath(lockDirectory)) {
        m_error = "Headroom could not create its instance directory.";
        return;
    }
    m_lockPath = QDir(lockDirectory).filePath("instance.lock");
    m_scopeName = "headroom-" + digest;
    m_pathsReady = true;
#else
    QString runtimePath = QStandardPaths::writableLocation(QStandardPaths::RuntimeLocation);
    ScopedFd runtime;
    ScopedFd headroom;
    ScopedFd scope;
    QString runtimeRoot;
#ifdef Q_OS_MACOS
    const QString configuredRuntime = qEnvironmentVariable("XDG_RUNTIME_DIR");
    if (configuredRuntime.isEmpty()) {
        // Darwin's sockaddr_un path is only 104 bytes. Use the short canonical
        // form of the system /tmp alias, then establish a private per-user root.
        runtimePath = QFileInfo(QStringLiteral("/tmp")).canonicalFilePath();
        if (runtimePath != QStringLiteral("/private/tmp")
            || !openSafeDirectory(runtimePath, false, runtime)
            || !openOrCreatePrivateDirectory(runtime.get(),
                QByteArrayLiteral("Headroom-") + QByteArray::number(geteuid()), headroom)) {
            m_error = "Headroom could not establish a private per-user runtime directory.";
            return;
        }
        runtimeRoot = QDir(runtimePath).filePath(QStringLiteral("Headroom-")
                                                + QString::number(geteuid()));
    } else {
        runtimePath = QDir::cleanPath(configuredRuntime);
        if (!openSafeDirectory(runtimePath, true, runtime)
            || !openOrCreatePrivateDirectory(runtime.get(), "Headroom", headroom)) {
            m_error = "Headroom could not establish a private per-user runtime directory.";
            return;
        }
        runtimeRoot = QDir(runtimePath).filePath(QStringLiteral("Headroom"));
    }
#else
    if (!openSafeDirectory(runtimePath, true, runtime)
        || !openOrCreatePrivateDirectory(runtime.get(), "Headroom", headroom)) {
        m_error = "Headroom could not establish a private per-user runtime directory.";
        return;
    }
    runtimeRoot = QDir(runtimePath).filePath(QStringLiteral("Headroom"));
#endif
    if (!openOrCreatePrivateDirectory(headroom.get(), digest.toLatin1(), scope)) {
        m_error = "Headroom could not establish a private per-user runtime directory.";
        return;
    }
    const QString lockDirectory = QDir(runtimeRoot).filePath(digest);
    m_lockPath = QDir(lockDirectory).filePath("lock");
    m_scopeName = QDir(lockDirectory).filePath("activate");
    if (QFile::encodeName(m_scopeName).size() >= qsizetype(sizeof(sockaddr_un::sun_path))) {
        m_lockPath.clear();
        m_scopeName.clear();
        m_error = "Headroom's private activation endpoint path is too long.";
        return;
    }
    m_pathsReady = true;
#endif
}

InstanceService::~InstanceService()
{
    if (m_server) { m_server->close(); delete m_server; }
    if (m_lock) { m_lock->unlock(); delete m_lock; }
}

QByteArray InstanceService::request(const QByteArray &message, int timeoutMilliseconds)
{
    constexpr qsizetype maximumReply = 1024 * 1024;
    m_primaryUnavailable = false;
    if (!m_pathsReady || message.isEmpty() || message.size() > 4095 || message.contains('\n') || message.contains('\0')) return {};
#ifndef Q_OS_WIN
    if (!safeEndpoint(m_scopeName, S_IFSOCK)) { m_error = "Unsafe local desktop endpoint."; return {}; }
    if (!safeEndpoint(m_lockPath, S_IFREG)) { m_error = "Unsafe local desktop lock."; return {}; }
#endif
    QLocalSocket socket;
    socket.setReadBufferSize(maximumReply + 1);
    socket.connectToServer(m_scopeName, QIODevice::ReadWrite);
    if (!socket.waitForConnected(timeoutMilliseconds)) {
        if (socket.error() == QLocalSocket::ServerNotFoundError || socket.error() == QLocalSocket::ConnectionRefusedError) {
            QLockFile probe(m_lockPath);
            probe.setStaleLockTime(0);
            if (probe.tryLock(0)) {
                probe.unlock();
                m_primaryUnavailable = true;
            }
        }
        m_error = "The local desktop is unavailable.";
        return {};
    }
    const QByteArray wire = message + '\n';
    if (socket.write(wire) != wire.size() || (socket.bytesToWrite() && !socket.waitForBytesWritten(timeoutMilliseconds))) return {};
    QElapsedTimer timer; timer.start();
    QByteArray response;
    while (timer.elapsed() < timeoutMilliseconds) {
        response += socket.read(maximumReply + 1 - response.size());
        if (response.size() > maximumReply || socket.bytesAvailable() > 0) return {};
        if (response.contains('\n')) return response.endsWith('\n') && response.count('\n') == 1 ? response : QByteArray();
        if (!socket.waitForReadyRead(qMax(1, timeoutMilliseconds - int(timer.elapsed()))) && socket.bytesAvailable() == 0) break;
    }
    m_error = "The local desktop did not acknowledge the request.";
    return {};
}

InstanceService::Result InstanceService::start(int timeoutMilliseconds)
{
    if (m_server || m_lock) return m_server ? Result::Primary : Result::Error;
    if (!m_pathsReady) return Result::Error;
#ifndef Q_OS_WIN
    if (!safeEndpoint(m_lockPath, S_IFREG) || !safeEndpoint(m_scopeName, S_IFSOCK)) {
        m_error = "Headroom refused an unsafe instance endpoint in its private runtime directory.";
        return Result::Error;
    }
#endif
    m_lock = new QLockFile(m_lockPath);
    m_lock->setStaleLockTime(0);
    if (!m_lock->tryLock(0)) {
        QLocalSocket socket;
        socket.setReadBufferSize(9);
        socket.connectToServer(m_scopeName, QIODevice::ReadWrite);
        if (!socket.waitForConnected(timeoutMilliseconds)) {
            m_error = "Another Headroom instance owns this settings scope but could not be activated.";
            delete m_lock; m_lock = nullptr;
            return Result::Error;
        }
        const QByteArray message = "activate\n";
        if (socket.write(message) != message.size() ||
            (socket.bytesToWrite() > 0 && !socket.waitForBytesWritten(timeoutMilliseconds) &&
             socket.bytesToWrite() > 0)) {
            m_error = "The running Headroom instance did not accept the activation request.";
            delete m_lock; m_lock = nullptr;
            return Result::Error;
        }
        QByteArray acknowledgement;
        QElapsedTimer timer; timer.start();
        while (!acknowledgement.contains('\n') && timer.elapsed() < timeoutMilliseconds) {
            acknowledgement += socket.read(qMax<qint64>(0, 9 - acknowledgement.size()));
            if (acknowledgement.size() > 8 || socket.bytesAvailable() > 0) break;
            if (acknowledgement.contains('\n')) break;
            const int remaining = timeoutMilliseconds - int(timer.elapsed());
            if (remaining <= 0 || (!socket.waitForReadyRead(remaining) && socket.bytesAvailable() == 0)) break;
        }
        acknowledgement += socket.read(qMax<qint64>(0, 9 - acknowledgement.size()));
        if (acknowledgement != "ok\n") {
            m_error = "The running Headroom instance did not acknowledge the activation request.";
            delete m_lock; m_lock = nullptr;
            return Result::Error;
        }
        socket.abort();
        delete m_lock; m_lock = nullptr;
        return Result::Secondary;
    }
    QLocalServer::removeServer(m_scopeName);
    m_server = new QLocalServer(this);
#ifdef Q_OS_WIN
    m_server->setSocketOptions(QLocalServer::UserAccessOption);
#endif
    if (!m_server->listen(m_scopeName)) {
        m_error = "Headroom could not create its private activation endpoint: " + m_server->errorString();
        m_lock->unlock(); delete m_lock; m_lock = nullptr;
        delete m_server; m_server = nullptr;
        return Result::Error;
    }
    connect(m_server, &QLocalServer::newConnection, this, [this] {
        while (auto socket = m_server->nextPendingConnection()) {
            socket->setReadBufferSize(4097);
            const auto readActivation = [this, socket] {
                if (socket->property("headroomActivationComplete").toBool()) return;
                QByteArray buffered = socket->property("headroomActivation").toByteArray();
                buffered += socket->read(qMax<qint64>(0, 4097 - buffered.size()));
                if (buffered.size() > 4096) {
                    socket->setProperty("headroomActivationComplete", true);
                    socket->disconnectFromServer(); return;
                }
                socket->setProperty("headroomActivation", buffered);
                if (buffered.contains('\n')) {
                    socket->setProperty("headroomActivationComplete", true);
                    if (buffered == "activate\n") {
                        emit activationRequested();
                        socket->write("ok\n");
                        socket->flush();
                    } else if (buffered.endsWith('\n') && buffered.count('\n') == 1 && m_requestHandler) {
                        const auto response = m_requestHandler(buffered.chopped(1));
                        if (!response.isEmpty() && response.size() <= 1024 * 1024 && !response.contains('\n')) {
                            socket->write(response + '\n');
                            socket->flush();
                        } else if (m_asyncRequestHandler) {
                            const QPointer<QLocalSocket> guarded(socket);
                            auto reply = [guarded](const QByteArray &value) {
                                if (!guarded || guarded->state() != QLocalSocket::ConnectedState) return;
                                if (!value.isEmpty() && value.size() <= 1024 * 1024 && !value.contains('\n')) {
                                    guarded->write(value + '\n'); guarded->flush();
                                }
                                guarded->disconnectFromServer();
                            };
                            if (m_asyncRequestHandler(buffered.chopped(1), std::move(reply))) {
                                socket->setProperty("headroomAsyncRequest", true);
                                QTimer::singleShot(8 * 60 * 1000, socket, [socket] { socket->disconnectFromServer(); });
                                return;
                            }
                        }
                    }
                    socket->disconnectFromServer();
                } else if (buffered.size() >= 4096 || socket->bytesAvailable() > 0) {
                    socket->setProperty("headroomActivationComplete", true);
                    socket->disconnectFromServer();
                }
            };
            connect(socket, &QLocalSocket::readyRead, this, readActivation);
            connect(socket, &QLocalSocket::disconnected, socket, &QObject::deleteLater);
            QMetaObject::invokeMethod(socket, readActivation, Qt::QueuedConnection);
            QTimer::singleShot(1000, socket, [socket] {
                if (!socket->property("headroomAsyncRequest").toBool()) socket->disconnectFromServer();
            });
        }
    });
    return Result::Primary;
}
