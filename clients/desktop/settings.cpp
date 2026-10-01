#include "settings.h"
#include "sshnetwork.h"

#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QSaveFile>
#include <QStandardPaths>
#include <QTemporaryFile>
#include <QUrl>
#ifdef Q_OS_WIN
#include <qt_windows.h>
#else
#include <unistd.h>
#endif

namespace {
constexpr int CurrentSchemaVersion = 1;

QString readString(const QJsonObject &object, const char *name, const QString &fallback = {})
{
    const auto value = object.value(QLatin1String(name));
    return value.isString() ? value.toString() : fallback;
}

bool readBool(const QJsonObject &object, const char *name, bool fallback)
{
    const auto value = object.value(QLatin1String(name));
    return value.isBool() ? value.toBool() : fallback;
}

int readInt(const QJsonObject &object, const char *name, int fallback)
{
    const auto value = object.value(QLatin1String(name));
    return value.isDouble() ? value.toInt() : fallback;
}

QStringList readOrder(const QJsonValue &value)
{
    QStringList result;
    if (!value.isArray()) return result;
    for (const auto &entry : value.toArray()) if (entry.isString()) result.append(entry.toString());
    return result;
}

bool validRemoteUrl(const QString &value)
{
    if (value.isEmpty()) return true;
    const QUrl url(value, QUrl::StrictMode);
    return url.isValid() && (url.scheme() == "http" || url.scheme() == "https") &&
           !url.host().isEmpty() && url.userInfo().isEmpty() && !url.hasQuery() && !url.hasFragment();
}

bool validToken(const QString &value)
{
    return !value.contains('\n') && !value.contains('\r');
}

bool createPrivateBackup(const QString &path, const QByteArray &contents)
{
    if (QFileInfo::exists(path)) return true;
    QFile backup(path);
    if (!backup.open(QIODevice::WriteOnly | QIODevice::NewOnly)) return false;
#ifndef Q_OS_WIN
    if (!backup.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner)) {
        backup.close(); QFile::remove(path); return false;
    }
#endif
    if (backup.write(contents) != contents.size()) {
        backup.close(); QFile::remove(path); return false;
    }
    backup.close();
    return true;
}

bool copyPreviousSettings(const QString &source, const QString &target,
                          const std::function<void()> &beforeInstall)
{
    const QString directory = QFileInfo(target).absolutePath();
    if (!QDir().mkpath(directory)) return false;
#ifndef Q_OS_WIN
    if (!QFile::setPermissions(directory, QFileDevice::ReadOwner | QFileDevice::WriteOwner | QFileDevice::ExeOwner))
        return false;
#endif
    QFile previous(source);
    if (!previous.open(QIODevice::ReadOnly)) return false;
    const QByteArray contents = previous.readAll();
    if (previous.error() != QFileDevice::NoError) return false;
    previous.close();
    QTemporaryFile temporary(QDir(directory).filePath(".settings-XXXXXX"));
    if (!temporary.open()) return false;
#ifndef Q_OS_WIN
    if (!temporary.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner)) return false;
#endif
    if (temporary.write(contents) != contents.size() || !temporary.flush()) return false;
    temporary.close();
    if (temporary.error() != QFileDevice::NoError) return false;
    if (beforeInstall) beforeInstall();
    // Native no-replace installation is atomic, including a concurrent writer.
#ifdef Q_OS_WIN
    const bool installed = MoveFileW(reinterpret_cast<LPCWSTR>(temporary.fileName().utf16()),
                                     reinterpret_cast<LPCWSTR>(target.utf16()));
#else
    const bool installed = ::link(QFile::encodeName(temporary.fileName()).constData(),
                                  QFile::encodeName(target).constData()) == 0;
#endif
    return installed || QFileInfo::exists(target);
}
}

SettingsService::SettingsService(QString path, bool allowAutomaticMigration,
                                 Platform platform, QString legacyPath, QString defaultPathOverride,
                                 QString previousPathOverride, std::function<void()> beforeMigrationInstall)
    : m_platform(platform), m_explicitPath(!path.isEmpty()),
      m_path(path.isEmpty() ? (defaultPathOverride.isEmpty() ? defaultPath(platform) : defaultPathOverride) : path),
      m_legacyPath(legacyPath.isEmpty() ? defaultLegacyPath() : std::move(legacyPath)),
      m_legacyBackupPath(m_path + ".legacy.bak"),
      m_allowAutomaticMigration(allowAutomaticMigration)
{
    m_value.order = normalizeOrder({}, m_value.primary);
    if (QFileInfo::exists(m_path)) {
        loadHeadroom();
    } else if (allowAutomaticMigration && !m_explicitPath) {
        const QString previous = previousPathOverride.isEmpty() ? previousDefaultPath(platform) : previousPathOverride;
        if (QFileInfo::exists(previous)) {
            if (!copyPreviousSettings(previous, m_path, beforeMigrationInstall)) {
                m_path = previous;
                m_legacyBackupPath = m_path + ".legacy.bak";
                m_migrationNotice = "Settings could not be copied; this session is using the previous settings location.";
            }
            loadHeadroom();
        } else if (isWindows()) {
            importLegacy();
        }
    }
}

bool SettingsService::isWindows() const
{
    if (m_platform == Platform::Windows) return true;
    if (m_platform == Platform::Linux || m_platform == Platform::Mac) return false;
#ifdef Q_OS_WIN
    return true;
#else
    return false;
#endif
}

QString SettingsService::defaultPath(Platform platform)
{
    bool windows = platform == Platform::Windows;
#ifdef Q_OS_WIN
    if (platform == Platform::Current) windows = true;
#endif
    if (windows) {
        QString base = qEnvironmentVariable("APPDATA");
        if (base.isEmpty())
            base = QFileInfo(QFileInfo(QStandardPaths::writableLocation(QStandardPaths::AppDataLocation)).path()).path();
        return QDir(base).filePath("Headroom/settings.json");
    }
    const QString xdg = qEnvironmentVariable("XDG_CONFIG_HOME");
    return QDir(xdg.isEmpty() ? QDir::homePath() + "/.config" : xdg).filePath("headroom/settings.json");
}

QString SettingsService::instanceIdentityPath()
{
    // Keep the pre-move identity so old/new generations and the CLI bridge find
    // the same running instance during updates. Only hashed; nothing is created here.
    return previousDefaultPath();
}

QString SettingsService::previousDefaultPath(Platform platform)
{
    bool mac = platform == Platform::Mac;
    bool windows = platform == Platform::Windows;
    if (platform == Platform::Current) {
#ifdef Q_OS_WIN
        windows = true;
#elif defined(Q_OS_MACOS)
        mac = true;
#endif
    }
    if (mac)
        return QDir(QDir::homePath()).filePath(QStringLiteral("Library/Application Support/Headroom/Headroom/settings.json"));
    // AppConfigLocation preserves the existing XDG path on Linux. AppDataLocation
    // is the current user's roaming application-data directory on Windows.
    const auto location = QStandardPaths::writableLocation(
        windows ? QStandardPaths::AppDataLocation : QStandardPaths::AppConfigLocation);
    return QDir(location).filePath("settings.json");
}

QString SettingsService::defaultLegacyPath()
{
#ifdef Q_OS_WIN
    return QDir(qEnvironmentVariable("APPDATA")).filePath("ClaudeUsageWidget/settings.json");
#else
    return {};
#endif
}

QString SettingsService::normalizeProvider(const QString &provider)
{
    const QString candidate = provider.trimmed();
    for (const QString &known : {QString("Claude"), QString("Codex"), QString("Cursor"), QString("Grok")})
        if (candidate.compare(known, Qt::CaseInsensitive) == 0) return known;
    return "Claude";
}

QStringList SettingsService::normalizeOrder(const QStringList &values, const QString &legacyPrimary)
{
    const QStringList defaults{"Claude", "Codex", "Cursor", "Grok"};
    QStringList result;
    for (const auto &value : values) {
        const QString normalized = normalizeProvider(value);
        const bool recognized = defaults.contains(value.trimmed(), Qt::CaseInsensitive);
        if (recognized && !result.contains(normalized)) result.append(normalized);
    }
    if (result.isEmpty()) result.append(normalizeProvider(legacyPrimary));
    for (const auto &provider : defaults) if (!result.contains(provider)) result.append(provider);
    return result;
}

bool SettingsService::loadHeadroom()
{
    QFile file(m_path);
    if (!file.open(QIODevice::ReadOnly)) {
        m_loadError = "Settings file could not be read and was not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    QJsonParseError parseError;
    const QByteArray original = file.readAll();
    file.close(); // Windows cannot atomically replace a file with an open read handle.
    const auto document = QJsonDocument::fromJson(original, &parseError);
    if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
        m_loadError = "Settings file is malformed and was not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    m_document = document.object();
    const int schemaVersion = readInt(m_document, "schemaVersion", 0);
    if (schemaVersion > CurrentSchemaVersion) {
        m_loadError = "Settings were written by a newer Headroom version and were not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    const QString rawUrl = readString(m_document, "url");
    const QString rawToken = readString(m_document, "token");
    const QString rawSshUrl = readString(m_document, "sshUrl");
    const QString url = rawUrl.trimmed();
    QString mode = readString(m_document, "connectionMode");
    const bool needsMode = mode != "local" && mode != "remote" && mode != "ssh";
    if (needsMode) mode = url.isEmpty() ? "local" : "remote";
    m_value.connectionMode = mode;
    m_value.url = url;
    m_value.token = rawToken.trimmed();
    m_value.sshUrl = rawSshUrl.trimmed();
    m_value.interval = qBound(15, readInt(m_document, "interval", 60), 900);
    m_value.notifications = readBool(m_document, "notifications", true);
    m_value.primary = normalizeProvider(readString(m_document, "primary", "Claude"));
    m_value.order = normalizeOrder(readOrder(m_document.value("order")), m_value.primary);
    m_value.primary = m_value.order.first();
    m_value.startup = readBool(m_document, "startup", false);
    m_value.startupMigrationPending = readBool(m_document, "startupMigrationPending", false);
    const int windowWidth = readInt(m_document, "windowWidth", 0);
    const int windowHeight = readInt(m_document, "windowHeight", 0);
    if (windowWidth > 0 && windowHeight > 0) m_value.windowSize = QSize(windowWidth, windowHeight);
    const bool invalidSsh = !m_value.sshUrl.isEmpty() && !SshTransport::parseAddress(m_value.sshUrl);
    if (invalidSsh && mode != QStringLiteral("ssh")) m_value.sshUrl.clear();
    if (!validRemoteUrl(url) || !validToken(rawToken) || (invalidSsh && mode == QStringLiteral("ssh"))) {
        m_loadError = "Settings contain an invalid backend address or token and were not changed.";
        m_blockImplicitWrites = true;
        m_value.url.clear();
        m_value.token.clear();
        m_value.sshUrl.clear();
        return false;
    }
    const bool needsSchema = schemaVersion < CurrentSchemaVersion;
    if (m_allowAutomaticMigration && (needsMode || needsSchema)) {
        const QString backupPath = m_path + ".bak";
        if (!createPrivateBackup(backupPath, original)) {
            m_loadError = "Could not preserve a backup of the existing settings.";
            m_blockImplicitWrites = true;
            return false;
        }
        const QString error = write(m_value);
        if (!error.isEmpty()) { m_loadError = error; m_blockImplicitWrites = true; return false; }
    }
    return true;
}

bool SettingsService::importLegacy()
{
    if (m_legacyPath.isEmpty() || !QFileInfo::exists(m_legacyPath)) return false;
    QFile legacy(m_legacyPath);
    if (!legacy.open(QIODevice::ReadOnly)) {
        m_loadError = "Legacy settings could not be read and were not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    const QByteArray original = legacy.readAll();
    QJsonParseError parseError;
    const auto document = QJsonDocument::fromJson(original, &parseError);
    if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
        m_loadError = "Legacy settings are malformed and were not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    const auto object = document.object();
    const int schema = readInt(object, "SchemaVersion", 0);
    if (schema < 0 || schema > 3) {
        m_loadError = "Legacy settings use an unsupported schema and were not changed.";
        m_blockImplicitWrites = true;
        return false;
    }
    DesktopSettings imported;
    const QString rawUrl = readString(object, "ApiUrl");
    const QString rawToken = readString(object, "ApiToken");
    imported.url = rawUrl.trimmed();
    imported.token = rawToken.trimmed();
    imported.connectionMode = rawUrl.isEmpty() ? "local" : "remote";
    imported.interval = qBound(15, readInt(object, "RefreshIntervalSeconds", 60), 900);
    imported.notifications = readBool(object, "NotificationsEnabled", true);
    imported.primary = normalizeProvider(readString(object, "PrimaryProvider", "Claude"));
    imported.order = normalizeOrder(readOrder(object.value("ProviderOrder")), imported.primary);
    imported.primary = imported.order.first();
    imported.startup = readBool(object, "StartWithWindows", false);
    imported.startupMigrationPending = imported.startup;
    if (!validRemoteUrl(imported.url) || !validToken(rawToken)) {
        m_loadError = "Legacy settings contain an invalid backend address or token and were not changed.";
        m_blockImplicitWrites = true;
        return false;
    }

    if (!QDir().mkpath(QFileInfo(m_path).absolutePath())) {
        m_loadError = "Could not create the Headroom settings directory.";
        m_blockImplicitWrites = true;
        return false;
    }
    if (!createPrivateBackup(m_legacyBackupPath, original)) {
        m_loadError = "Could not preserve a backup of the legacy settings.";
        m_blockImplicitWrites = true;
        return false;
    }
    const QString error = write(imported);
    if (!error.isEmpty()) {
        m_loadError = error;
        m_blockImplicitWrites = true;
        return false;
    }
    m_value = imported;
    m_importedLegacy = true;
    return true;
}

QJsonObject SettingsService::serialized(const DesktopSettings &settings) const
{
    QJsonObject result = m_document;
    result["schemaVersion"] = CurrentSchemaVersion;
    result["connectionMode"] = settings.connectionMode;
    result["url"] = settings.url;
    result["token"] = settings.token;
    result["sshUrl"] = settings.sshUrl;
    result["interval"] = settings.interval;
    result["notifications"] = settings.notifications;
    result["primary"] = settings.primary;
    result["order"] = QJsonArray::fromStringList(settings.order);
    result["startup"] = settings.startup;
    result["startupMigrationPending"] = settings.startupMigrationPending;
    if (settings.windowSize.isValid()) {
        result["windowWidth"] = settings.windowSize.width();
        result["windowHeight"] = settings.windowSize.height();
    } else {
        result.remove("windowWidth");
        result.remove("windowHeight");
    }
    return result;
}

QString SettingsService::write(const DesktopSettings &settings)
{
    if (!QDir().mkpath(QFileInfo(m_path).absolutePath())) return "Could not create the settings directory.";
    QSaveFile file(m_path);
    if (!file.open(QIODevice::WriteOnly)) return "Could not save settings. Check directory permissions.";
#ifndef Q_OS_WIN
    if (!file.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner))
        return "Could not secure the settings file.";
#endif
    const auto object = serialized(settings);
    const QByteArray data = QJsonDocument(object).toJson();
    if (file.write(data) != data.size() || !file.commit())
        return "Could not save settings. Your previous configuration is unchanged.";
    m_document = object;
    return {};
}

QString SettingsService::save(const DesktopSettings &settings, bool explicitUserSave)
{
    if (m_blockImplicitWrites && !explicitUserSave) return m_loadError;
    DesktopSettings normalized = settings;
    if (normalized.connectionMode != "local" && normalized.connectionMode != "remote" && normalized.connectionMode != "ssh")
        normalized.connectionMode = "remote";
    normalized.url = normalized.url.trimmed();
    normalized.token = normalized.token.trimmed();
    normalized.sshUrl = normalized.sshUrl.trimmed();
    if (!validToken(settings.token)) return "The bearer token must be a single line.";
    if (normalized.connectionMode == "remote" && !validRemoteUrl(normalized.url))
        return "Use an HTTP or HTTPS address without credentials, a query, or a fragment.";
    if (normalized.connectionMode == "ssh" && !SshTransport::parseAddress(normalized.sshUrl))
        return "Use ssh://[user@]host[:port] without a password, path, query, or fragment.";
    normalized.interval = qBound(15, normalized.interval, 900);
    normalized.order = normalizeOrder(normalized.order, normalized.primary);
    normalized.primary = normalized.order.first();
    const QString error = write(normalized);
    if (error.isEmpty()) {
        m_value = normalized;
        m_loadError.clear();
        m_blockImplicitWrites = false;
    }
    return error;
}

QString SettingsService::saveOrder(const QStringList &order, const QString &primary)
{
    DesktopSettings updated = m_value;
    updated.order = normalizeOrder(order, primary);
    updated.primary = updated.order.first();
    return save(updated);
}

QString SettingsService::saveStartupPreference(bool enabled)
{
    DesktopSettings updated = m_value;
    updated.startup = enabled;
    return save(updated);
}

QString SettingsService::completeStartupMigration()
{
    DesktopSettings updated = m_value;
    updated.startupMigrationPending = false;
    return save(updated);
}

QString SettingsService::saveWindowSize(const QSize &size)
{
    if (!size.isValid()) return QStringLiteral("Could not save an invalid window size.");
    DesktopSettings updated = m_value;
    updated.windowSize = size;
    return save(updated);
}
