#pragma once

#include <QObject>
#include <QProcess>
#include <QTimer>
#include <QIcon>
#include <QRect>
#include <QJsonObject>
#include <QByteArray>

// The installed Windows launcher owns the Shell icon at a stable executable
// path. The desktop retains rendering, menus, notifications and popup behavior.
class StableTray : public QObject {
    Q_OBJECT
public:
    explicit StableTray(QObject *parent = nullptr);
    ~StableTray() override;
    void start(const QString &launcher);
    void stop();
    void setIcon(const QIcon &icon);
    void setToolTip(const QString &text);
    void showMessage(const QString &title, const QString &message, int severity);
    void restoreFocus();
    bool available() const { return m_available; }
    bool hovered() const { return m_hovered; }
    QRect geometry() const;
    // UUID v5-style identity shared with the launcher tray host.
    static QByteArray shellIdentity(const QString &launcher);
signals:
    void availabilityChanged(bool available);
    void activated(int reason);
    void messageClicked();
private:
    void send(QJsonObject command);
    void sendIcon();
    void readEvents();
    void failed();
    void removeShellIcon();
    QProcess m_process;
    QTimer m_timeout;
    QIcon m_icon;
    QString m_tooltip;
    QString m_launcher;
    QByteArray m_output;
    QRect m_geometry;
    int m_size = 32;
    bool m_attempted = false;
    bool m_available = false;
    bool m_hovered = false;
    bool m_stopping = false;
};
