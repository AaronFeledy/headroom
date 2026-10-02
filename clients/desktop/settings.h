#pragma once

#include <QJsonObject>
#include <QSize>
#include <QString>
#include <QStringList>
#include <functional>

struct DesktopSettings {
    QString connectionMode = "local";
    QString url;
    QString token;
    QString sshUrl;
    QString remoteCertificate;
    bool shareBrowserSignIns = true;
    int interval = 60;
    bool notifications = true;
    QString primary = "Claude";
    QStringList order;
    bool startup = false;
    bool startupMigrationPending = false;
    QSize windowSize;
};

class SettingsService {
public:
    enum class Platform { Current, Linux, Mac, Windows };

    explicit SettingsService(QString path = {}, bool allowAutomaticMigration = true,
                             Platform platform = Platform::Current, QString legacyPath = {},
                             QString defaultPathOverride = {}, QString previousPathOverride = {},
                             std::function<void()> beforeMigrationInstall = {});

    const DesktopSettings &value() const { return m_value; }
    QString path() const { return m_path; }
    QString legacyBackupPath() const { return m_legacyBackupPath; }
    QString loadError() const { return m_loadError; }
    QString migrationNotice() const { return m_migrationNotice; }
    bool importedLegacy() const { return m_importedLegacy; }

    QString save(const DesktopSettings &settings, bool explicitUserSave = false);
    QString saveOrder(const QStringList &order, const QString &primary);
    QString saveStartupPreference(bool enabled);
    QString completeStartupMigration();
    QString saveWindowSize(const QSize &size);

    static QString defaultPath(Platform platform = Platform::Current);
    static QString previousDefaultPath(Platform platform = Platform::Current);
    static QString instanceIdentityPath();
    static QString defaultLegacyPath();
    static QStringList normalizeOrder(const QStringList &order, const QString &legacyPrimary = {});
    static QString normalizeProvider(const QString &provider);

private:
    bool isWindows() const;
    bool loadHeadroom();
    bool importLegacy();
    QString write(const DesktopSettings &settings);
    QJsonObject serialized(const DesktopSettings &settings) const;

    Platform m_platform;
    bool m_explicitPath = false;
    QString m_path;
    QString m_legacyPath;
    QString m_legacyBackupPath;
    QString m_loadError;
    QString m_migrationNotice;
    QJsonObject m_document;
    DesktopSettings m_value;
    bool m_importedLegacy = false;
    bool m_blockImplicitWrites = false;
    bool m_allowAutomaticMigration = true;
};
