#include "stabletray.h"

#include <QGuiApplication>
#include <QScreen>
#include <QJsonDocument>
#include <QImage>
#ifdef Q_OS_WIN
#include <qt_windows.h>
#endif

StableTray::StableTray(QObject *parent) : QObject(parent) {
    m_timeout.setSingleShot(true);
    m_timeout.setInterval(15000);
    connect(&m_timeout, &QTimer::timeout, this, &StableTray::failed);
    connect(&m_process, &QProcess::started, this, [this] {
        setToolTip(m_tooltip);
        sendIcon();
    });
    connect(&m_process, &QProcess::readyReadStandardOutput, this, &StableTray::readEvents);
    // Never surface helper output containing notification or tooltip contents.
    connect(&m_process, &QProcess::readyReadStandardError, this, [this] { m_process.readAllStandardError(); });
    connect(&m_process, &QProcess::errorOccurred, this, [this] { failed(); });
    connect(&m_process, &QProcess::finished, this, [this] { failed(); });
}

StableTray::~StableTray() { stop(); }

void StableTray::start(const QString &launcher) {
    if (m_attempted || launcher.isEmpty()) return;
    m_attempted = true;
    m_timeout.start();
    m_process.start(launcher, {QStringLiteral("--headroom-tray-host")});
}

void StableTray::stop() {
    if (m_stopping) return;
    m_stopping = true;
    m_timeout.stop();
    if (m_process.state() != QProcess::NotRunning) {
        send({{QStringLiteral("op"), QStringLiteral("quit")}});
        m_process.closeWriteChannel();
        if (!m_process.waitForFinished(1500)) {
            // Only terminate the child created by this QProcess.
            m_process.kill();
            m_process.waitForFinished(1500);
        }
    }
    m_available = false;
    m_hovered = false;
    m_stopping = false;
}

void StableTray::failed() {
    if (m_stopping) return;
    const bool wasAvailable = m_available;
    stop();
    if (wasAvailable) emit availabilityChanged(false);
}

void StableTray::send(QJsonObject command) {
    if (m_process.state() != QProcess::Running) return;
    command.insert(QStringLiteral("version"), 1);
    const auto data = QJsonDocument(command).toJson(QJsonDocument::Compact) + '\n';
    if (data.size() > 32768 || m_process.bytesToWrite() > 131072) { failed(); return; }
    m_process.write(data);
}

void StableTray::setIcon(const QIcon &icon) { m_icon = icon; sendIcon(); }

void StableTray::sendIcon() {
    if (m_icon.isNull() || m_process.state() != QProcess::Running) return;
    QImage image = m_icon.pixmap(QSize(m_size, m_size), 1.0).toImage()
                       .convertToFormat(QImage::Format_ARGB32_Premultiplied);
    if (image.size() != QSize(m_size, m_size)) image = image.scaled(m_size, m_size, Qt::IgnoreAspectRatio, Qt::SmoothTransformation);
    // Windows on x64 and ARM64 is little endian: ARGB32 memory is BGRA.
    QByteArray pixels;
    pixels.reserve(m_size * m_size * 4);
    for (int row = 0; row < m_size; ++row) pixels.append(reinterpret_cast<const char *>(image.constScanLine(row)), m_size * 4);
    send({{QStringLiteral("op"), QStringLiteral("icon")}, {QStringLiteral("size"), m_size},
          {QStringLiteral("pixels"), QString::fromLatin1(pixels.toBase64())}});
}

void StableTray::setToolTip(const QString &text) {
    m_tooltip = text;
    send({{QStringLiteral("op"), QStringLiteral("tooltip")}, {QStringLiteral("tooltip"), text.left(512)}});
}

void StableTray::showMessage(const QString &title, const QString &message, int severity) {
    send({{QStringLiteral("op"), QStringLiteral("notify")}, {QStringLiteral("title"), title.left(128)},
          {QStringLiteral("message"), message.left(1000)}, {QStringLiteral("severity"), qBound(0, severity, 3)}});
}

void StableTray::restoreFocus() { send({{QStringLiteral("op"), QStringLiteral("focus")}}); }

void StableTray::readEvents() {
    m_output += m_process.readAllStandardOutput();
    if (m_output.size() > 32768) { failed(); return; }
    while (m_output.contains('\n')) {
        const auto end = m_output.indexOf('\n');
        const auto line = m_output.left(end);
        m_output.remove(0, end + 1);
        QJsonParseError error;
        const auto document = QJsonDocument::fromJson(line, &error);
        const auto event = document.object();
        if (error.error != QJsonParseError::NoError || !document.isObject() || event.value(QStringLiteral("version")).toInt() != 1) { failed(); return; }
        const auto kind = event.value(QStringLiteral("event")).toString();
        if (kind == QStringLiteral("ready") || kind == QStringLiteral("size")) {
            const int size = event.value(QStringLiteral("size")).toInt();
            if (size < 16 || size > 64) { failed(); return; }
            if (kind == QStringLiteral("ready") && !m_available) {
                m_available = true;
                m_timeout.stop();
                emit availabilityChanged(true);
            }
            if (m_size != size) { m_size = size; sendIcon(); }
        } else if (kind == QStringLiteral("geometry")) {
            m_geometry = QRect(event.value(QStringLiteral("x")).toInt(), event.value(QStringLiteral("y")).toInt(),
                               event.value(QStringLiteral("width")).toInt(), event.value(QStringLiteral("height")).toInt());
            m_hovered = event.value(QStringLiteral("hover")).toBool();
        } else if (kind == QStringLiteral("activate")) {
            const int reason = event.value(QStringLiteral("reason")).toInt();
            if (m_available && reason >= 1 && reason <= 4) emit activated(reason);
        } else if (kind == QStringLiteral("message")) {
            if (m_available) emit messageClicked();
        } else { failed(); return; }
    }
}

QRect StableTray::geometry() const {
#ifdef Q_OS_WIN
    if (!m_geometry.isValid()) return {};
    RECT bounds{m_geometry.left(), m_geometry.top(), m_geometry.x() + m_geometry.width(), m_geometry.y() + m_geometry.height()};
    MONITORINFOEXW info{};
    info.cbSize = sizeof(info);
    if (GetMonitorInfoW(MonitorFromRect(&bounds, MONITOR_DEFAULTTONEAREST), &info)) {
        for (const auto *screen : QGuiApplication::screens()) {
            if (screen->name().compare(QString::fromWCharArray(info.szDevice), Qt::CaseInsensitive) != 0) continue;
            const qreal scale = screen->devicePixelRatio();
            return QRect(screen->geometry().topLeft() + QPoint(qRound((m_geometry.x() - info.rcMonitor.left) / scale),
                                                               qRound((m_geometry.y() - info.rcMonitor.top) / scale)),
                         QSize(qRound(m_geometry.width() / scale), qRound(m_geometry.height() / scale)));
        }
    }
#endif
    return {};
}
