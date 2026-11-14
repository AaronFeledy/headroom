#include "managedserver.h"

#include <QDir>
#include <QFileInfo>
#include <utility>

namespace {
bool sameConfig(const QString &owned, const QString &requested)
{
    if (owned.isEmpty() || !QDir::isAbsolutePath(requested)) return false;
    const QString left = QFileInfo(owned).canonicalFilePath();
    const QString right = QFileInfo(requested).canonicalFilePath();
#ifdef Q_OS_WIN
    constexpr auto sensitivity = Qt::CaseInsensitive;
#else
    constexpr auto sensitivity = Qt::CaseSensitive;
#endif
    return !left.isEmpty() && !right.isEmpty() && left.compare(right, sensitivity) == 0;
}
}

void ManagedServer::requestRestart(const QJsonObject &request, bool updateBusy,
                                 std::function<void(const QJsonObject &)> reply)
{
    if (request.value("schema").toInt() != 1 || request.value("product").toString() != "Headroom"
        || request.value("command").toString() != "restart-server"
        || !request.value("config_path").isString() || !QDir::isAbsolutePath(request.value("config_path").toString())) {
        reply({{"ok", false}, {"error", "Invalid server restart request."}});
        return;
    }
    if (updateBusy || m_restartRequest) {
        reply({{"ok", true}, {"result", "busy"}});
        return;
    }
    if (m_mode != QStringLiteral("local") || !ownsProcess()) {
        reply({{"ok", true}, {"result", "not_owner"}});
        return;
    }
    const QString requested = request.value("config_path").toString();
    if (!sameConfig(ownedConfigPath(), requested)) {
        reply({{"ok", true}, {"result", "other_config"}, {"config_path", ownedConfigPath()}});
        return;
    }
    auto operation = new QObject(this);
    m_restartRequest = operation;
    auto deadline = new QTimer(operation);
    deadline->setSingleShot(true);
    deadline->setTimerType(Qt::PreciseTimer);
    auto finish = [this, operation, deadline, reply = std::move(reply)](const QJsonObject &response) {
        if (m_restartRequest != operation) return;
        m_restartRequest.clear();
        deadline->stop();
        disconnect(this, nullptr, operation, nullptr);
        operation->deleteLater();
        reply(response);
    };
    connect(this, &ManagedServer::available, operation, [this, requested, finish] {
        if (!ownsProcess() || !sameConfig(ownedConfigPath(), requested)) {
            finish({{"ok", false}, {"error", "The replacement server's ownership or configuration could not be confirmed."}});
            return;
        }
        finish({{"ok", true}, {"result", "restarted"}, {"config_path", ownedConfigPath()}});
    });
    connect(this, &ManagedServer::unavailable, operation, [finish](const QString &message, const QString &) {
        finish({{"ok", false}, {"error", message}});
    });
    connect(deadline, &QTimer::timeout, operation, [this, finish] {
        const QString message = QStringLiteral("The local server did not become available within %1 seconds.")
                                    .arg(m_options.restartTimeoutMs / 1000.0);
        ++m_generation;
        cancelAsync();
        m_ensureAfterRetire = false;
        disposeProcess(true);
        if (m_mode == QStringLiteral("local")) setFailure(message, QStringLiteral("timeout"));
        else finish({{"ok", false}, {"error", message}});
    });
    deadline->start(m_options.restartTimeoutMs);
    stopOwned();
    ensureAvailable();
}
