#pragma once
#include <QObject>
#include <QVariantList>
#include <QSet>
#include <QHash>

// Session-only attention: no usage details or notification history are persisted.
class Notifications : public QObject {
    Q_OBJECT
    Q_PROPERTY(int unreadCount READ unreadCount NOTIFY pendingChanged)
    Q_PROPERTY(QVariantList presented READ presented NOTIFY presentationChanged)
public:
    explicit Notifications(QObject *parent = nullptr) : QObject(parent) {}
    static constexpr int MaxPending = 128;
    int unreadCount() const { return m_pending.size(); }
    QVariantList presented() const { return m_presented; }
    void post(const QString &target, const QString &title, const QString &message, int severity = 0, const QVariantMap &details = {});
    void observeUpdate(const QString &state, const QString &version, const QString &message, bool enabled);
    void observeBankedResetCount(qint64 count, const QString &accountFingerprint, bool enabled);
    void resetBankedResetBaseline();
    Q_INVOKABLE void present();
    Q_INVOKABLE void endPresentation();
    Q_INVOKABLE bool claimHighlight(const QString &target);
signals:
    void pendingChanged();
    void presentationChanged();
    void desktopNotification(const QString &title, const QString &message, int severity);
private:
    qint64 m_bankedResetCount = -1;
    QString m_resetAccountFingerprint;
    QVariantList m_pending, m_presented;
    QSet<QString> m_highlights;
    QHash<QString, QString> m_updateEpisodes;
};
