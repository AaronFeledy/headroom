#include "popup.h"
#include <QGuiApplication>
#include <QCursor>
#include <QKeyEvent>
#include <QMouseEvent>
#include <QQuickWindow>
#include <QScreen>
#include <algorithm>
#ifdef HEADROOM_LAYER_SHELL
#include <LayerShellQt/Window>
#endif

namespace {
constexpr int PopupMargin = 24;
constexpr QSize PopupMinimum(460, 420);
constexpr QSize PopupDefault(539, 820); // Logical pixels; Qt applies display scaling.
constexpr QSize PopupMaximum(1600, 1200);

QRect insetWorkArea(const QRect &available)
{
    const QRect inset = available.adjusted(PopupMargin, PopupMargin, -PopupMargin, -PopupMargin);
    return inset.isValid() ? inset : available;
}

Qt::CursorShape resizeCursor(Qt::Edges edges)
{
    if (edges == (Qt::TopEdge | Qt::LeftEdge) || edges == (Qt::BottomEdge | Qt::RightEdge))
        return Qt::SizeFDiagCursor;
    if (edges == (Qt::TopEdge | Qt::RightEdge) || edges == (Qt::BottomEdge | Qt::LeftEdge))
        return Qt::SizeBDiagCursor;
    if (edges.testFlag(Qt::LeftEdge) || edges.testFlag(Qt::RightEdge)) return Qt::SizeHorCursor;
    if (edges.testFlag(Qt::TopEdge) || edges.testFlag(Qt::BottomEdge)) return Qt::SizeVerCursor;
    return Qt::ArrowCursor;
}
}

QSize PopupPlacement::constrainedSize(QSize available, QSize preferred)
{
    const QSize maximum(qMin(PopupMaximum.width(), available.width()),
                        qMin(PopupMaximum.height(), available.height()));
    const QSize minimum(qMin(PopupMinimum.width(), maximum.width()),
                        qMin(PopupMinimum.height(), maximum.height()));
    if (!preferred.isValid()) preferred = PopupDefault;
    return preferred.expandedTo(minimum).boundedTo(maximum);
}

Qt::Edges PopupPlacement::resizeEdges(QSize window, QPoint position, int margin)
{
    Qt::Edges edges;
    if (position.x() >= 0 && position.x() < margin) edges |= Qt::LeftEdge;
    else if (position.x() < window.width() && position.x() >= window.width() - margin) edges |= Qt::RightEdge;
    if (position.y() >= 0 && position.y() < margin) edges |= Qt::TopEdge;
    else if (position.y() < window.height() && position.y() >= window.height() - margin) edges |= Qt::BottomEdge;
    return edges;
}

QRect PopupPlacement::resizedBounds(QRect start, Qt::Edges edges, QPoint delta, const QRect &work,
                                    QSize minimum, QSize maximum)
{
    QRect rect = start;
    if (edges.testFlag(Qt::LeftEdge)) rect.setLeft(rect.left() + delta.x());
    if (edges.testFlag(Qt::RightEdge)) rect.setRight(rect.right() + delta.x());
    if (edges.testFlag(Qt::TopEdge)) rect.setTop(rect.top() + delta.y());
    if (edges.testFlag(Qt::BottomEdge)) rect.setBottom(rect.bottom() + delta.y());
    minimum = minimum.boundedTo(work.size());
    maximum = maximum.boundedTo(work.size());
    const int width = qBound(minimum.width(), rect.width(), maximum.width());
    const int height = qBound(minimum.height(), rect.height(), maximum.height());
    if (edges.testFlag(Qt::LeftEdge)) rect.setLeft(rect.right() - width + 1); else rect.setWidth(width);
    if (edges.testFlag(Qt::TopEdge)) rect.setTop(rect.bottom() - height + 1); else rect.setHeight(height);
    if (rect.left() < work.left()) rect.moveLeft(work.left());
    if (rect.right() > work.right()) rect.moveRight(work.right());
    if (rect.top() < work.top()) rect.moveTop(work.top());
    if (rect.bottom() > work.bottom()) rect.moveBottom(work.bottom());
    return rect;
}

QRect PopupPlacement::bounds(const QRect &screen, const QRect &available, QPoint anchor, QSize preferred) {
    const QRect work = insetWorkArea(available);
    preferred = constrainedSize(work.size(), preferred);
    if (!screen.contains(anchor)) anchor = QPoint(available.right() - 24, screen.bottom() - 12);
    const int distances[] = {qAbs(anchor.y() - screen.top()), qAbs(screen.bottom() - anchor.y()),
                             qAbs(anchor.x() - screen.left()), qAbs(screen.right() - anchor.x())};
    const int edge = int(std::min_element(std::begin(distances), std::end(distances)) - std::begin(distances));
    QPoint pos(anchor.x() - preferred.width() / 2, anchor.y() - preferred.height() / 2);
    if (edge == 0) pos.setY(anchor.y() + 24);
    if (edge == 1) pos.setY(anchor.y() - 24 - preferred.height());
    if (edge == 2) pos.setX(anchor.x() + 24);
    if (edge == 3) pos.setX(anchor.x() - 24 - preferred.width());
    pos.setX(qBound(work.left(), pos.x(), work.right() - preferred.width() + 1));
    pos.setY(qBound(work.top(), pos.y(), work.bottom() - preferred.height() + 1));
    return QRect(pos, preferred);
}
TrayPopup::TrayPopup(QQuickWindow *window, bool attached, QSize preferred, SizeWriter sizeWriter, QObject *parent)
    : QObject(parent), m_window(window), m_attached(attached),
      m_preferred(preferred.isValid() ? preferred : (attached ? PopupDefault : window->size())),
      m_sizeWriter(std::move(sizeWriter)) {
    m_window->installEventFilter(this);
    m_saveSize.setSingleShot(true);
    m_saveSize.setInterval(350);
    connect(&m_saveSize, &QTimer::timeout, this, [this] {
        const QSize selected = m_pendingUserSize.isValid() ? m_pendingUserSize : effectiveGeometry().size();
        if (!m_resizing && m_sizeWriter && selected.isValid()) {
            m_preferred = selected;
            m_pendingUserSize = {};
            m_sizeWriter(m_preferred);
        }
    });
    const auto sizeChanged = [this] {
        if (m_expectedProgrammaticSize.isValid()
            && (m_window->width() == m_expectedProgrammaticSize.width()
                || m_window->height() == m_expectedProgrammaticSize.height())) {
            if (m_window->size() == m_expectedProgrammaticSize) m_expectedProgrammaticSize = {};
            if (!m_pendingUserSize.isValid()) m_saveSize.stop();
            return;
        }
        if (m_window->isVisible() && m_sizeWriter) {
            m_pendingUserSize = effectiveGeometry().size();
            m_saveSize.start();
        }
    };
    connect(window, &QWindow::widthChanged, this, sizeChanged);
    connect(window, &QWindow::heightChanged, this, sizeChanged);
    connect(window, &QWindow::widthChanged, this, &TrayPopup::syncLayerConfigure);
    connect(window, &QWindow::heightChanged, this, &TrayPopup::syncLayerConfigure);
    connect(window, &QWindow::visibleChanged, this, [this](bool visible) {
        if (!visible) writePendingSize();
    });
    connect(qGuiApp, &QCoreApplication::aboutToQuit, this, &TrayPopup::writePendingSize);
    connect(window, &QWindow::screenChanged, this, [this](QScreen *screen) {
        watchScreen(screen);
        if (m_window->isVisible()) constrainToScreen(screen);
    });
    connect(window, &QWindow::windowStateChanged, this, [this](Qt::WindowState state) {
        if (state == Qt::WindowFullScreen || state == Qt::WindowMaximized)
            m_window->setWindowState(Qt::WindowNoState);
    });
    m_resizePoll.setInterval(50);
    connect(&m_resizePoll, &QTimer::timeout, this, [this] {
        if (!(QGuiApplication::mouseButtons() & Qt::LeftButton)) finishResize();
    });

    if (!attached) return;
#ifdef HEADROOM_LAYER_SHELL
    if (QGuiApplication::platformName().startsWith("wayland")) {
        auto layer = LayerShellQt::Window::get(window);
        layer->setScope("headroom-tray-popup");
        layer->setLayer(LayerShellQt::Window::LayerTop);
        layer->setExclusiveZone(-1);
        layer->setKeyboardInteractivity(LayerShellQt::Window::KeyboardInteractivityOnDemand);
        layer->setActivateOnShow(true);
        layer->setAnchors(LayerShellQt::Window::Anchors(LayerShellQt::Window::AnchorTop) | LayerShellQt::Window::AnchorLeft);
    }
#endif
    m_dismiss.setSingleShot(true); m_dismiss.setInterval(150);
    connect(qGuiApp, &QGuiApplication::focusWindowChanged, this, [this](QWindow *) {
        if (!m_resizing && m_window->isVisible() && !m_window->isActive()) m_dismiss.start();
    });
    connect(window, &QWindow::activeChanged, this, [this] {
        if (m_window->isActive()) m_dismiss.stop();
        else if (!m_resizing && m_window->isVisible()) m_dismiss.start();
    });
    connect(&m_dismiss, &QTimer::timeout, this, [this] {
        for (auto focus = QGuiApplication::focusWindow(); focus; focus = focus->transientParent())
            if (focus == m_window) return;
        m_window->hide();
    });
}

TrayPopup::~TrayPopup()
{
    writePendingSize();
}

void TrayPopup::watchScreen(QScreen *screen)
{
    disconnect(m_workAreaConnection);
    if (!screen) return;
    m_workAreaConnection = connect(screen, &QScreen::availableGeometryChanged, this, [this, screen] {
        if (m_window->isVisible() && m_window->screen() == screen) constrainToScreen(screen);
    });
}

void TrayPopup::position() {
    auto screen = m_hasAnchor ? QGuiApplication::screenAt(m_anchor) : QGuiApplication::screenAt(QCursor::pos());
    if (!screen) screen = QGuiApplication::primaryScreen();
    if (!screen) return;
    const auto full = screen->geometry(), work = screen->availableGeometry();
    const QSize size = PopupPlacement::constrainedSize(insetWorkArea(work).size(), m_preferred);
    m_window->setScreen(screen);
    watchScreen(screen);
    m_window->setMinimumSize(QSize(qMin(PopupMinimum.width(), size.width()), qMin(PopupMinimum.height(), size.height())));
    m_window->setMaximumSize(QSize(qMin(PopupMaximum.width(), insetWorkArea(work).width()),
                                   qMin(PopupMaximum.height(), insetWorkArea(work).height())));
    if (!m_attached) {
        QRect rect(m_window->position(), size);
        const QRect reachable = insetWorkArea(work);
        rect.moveLeft(qBound(reachable.left(), rect.left(), reachable.right() - rect.width() + 1));
        rect.moveTop(qBound(reachable.top(), rect.top(), reachable.bottom() - rect.height() + 1));
        applyGeometry(rect, screen);
        return;
    }
    const QPoint anchor = m_hasAnchor ? m_anchor : QPoint(work.right() - 24, full.bottom() - 12);
    const auto rect = PopupPlacement::bounds(full, work, anchor, size);
    if (usesLayerShell()) {
        applyGeometry(rect, screen);
        return;
    }
    m_window->resize(rect.size());
    m_window->setPosition(rect.topLeft());
}

bool TrayPopup::eventFilter(QObject *watched, QEvent *event)
{
    if (watched != m_window) return QObject::eventFilter(watched, event);
    if (event->type() == QEvent::KeyPress
        && static_cast<QKeyEvent *>(event)->key() == Qt::Key_Escape) {
        QMetaObject::invokeMethod(m_window, "dismissOverlayOrHide");
        return true;
    }
    if (event->type() != QEvent::MouseMove && event->type() != QEvent::MouseButtonPress
        && event->type() != QEvent::MouseButtonRelease && event->type() != QEvent::Leave)
        return QObject::eventFilter(watched, event);

    if (event->type() == QEvent::Leave) {
        if (!m_resizing) m_window->unsetCursor();
        return false;
    }
    auto *mouse = static_cast<QMouseEvent *>(event);
    const Qt::Edges edges = PopupPlacement::resizeEdges(m_window->size(), mouse->position().toPoint());
    if (event->type() == QEvent::MouseMove && !m_resizing) {
        m_window->setCursor(QCursor(resizeCursor(edges)));
        return false;
    }
    if (event->type() == QEvent::MouseButtonPress && mouse->button() == Qt::LeftButton && edges) {
        m_dismiss.stop();
        m_saveSize.stop();
        m_resizing = true;
        m_resizeEdges = edges;
        m_resizeStartGeometry = effectiveGeometry();
        m_resizeStartPointer = resizePointer(mouse);
        const QString platform = QGuiApplication::platformName();
        m_nativeResize = platform != QStringLiteral("xcb") && !usesLayerShell()
            && m_window->startSystemResize(edges);
        m_resizePoll.start();
        return true;
    }
    if (event->type() == QEvent::MouseMove && m_resizing && !m_nativeResize) {
        applyFallbackResize(resizePointer(mouse));
        return true;
    }
    if (event->type() == QEvent::MouseButtonRelease && m_resizing) {
        finishResize();
        return true;
    }
    return QObject::eventFilter(watched, event);
}

void TrayPopup::applyFallbackResize(const QPoint &globalPosition)
{
    const QPoint delta = globalPosition - m_resizeStartPointer;
    QScreen *screen = m_window->screen();
    if (!screen) screen = QGuiApplication::screenAt(globalPosition);
    if (!screen) return;
    const QRect work = insetWorkArea(screen->availableGeometry());
    const QRect rect = PopupPlacement::resizedBounds(m_resizeStartGeometry, m_resizeEdges, delta, work);
    applyGeometry(rect, screen);
}

void TrayPopup::finishResize()
{
    if (!m_resizing) return;
    m_resizing = false;
    m_nativeResize = false;
    m_resizePoll.stop();
    constrainToScreen(m_window->screen());
    m_preferred = effectiveGeometry().size();
    m_pendingUserSize = m_preferred;
    m_window->unsetCursor();
    if (m_sizeWriter) m_saveSize.start();
    if (m_attached && m_window->isVisible() && !m_window->isActive()) m_dismiss.start();
}

void TrayPopup::constrainToScreen(QScreen *screen)
{
    if (!screen) screen = QGuiApplication::primaryScreen();
    if (!screen) return;
    const QRect work = insetWorkArea(screen->availableGeometry());
    const QSize maximum(qMin(PopupMaximum.width(), work.width()), qMin(PopupMaximum.height(), work.height()));
    QRect rect = effectiveGeometry();
    rect.setSize(PopupPlacement::constrainedSize(work.size(), rect.size()));
    if (rect.left() < work.left()) rect.moveLeft(work.left());
    if (rect.right() > work.right()) rect.moveRight(work.right());
    if (rect.top() < work.top()) rect.moveTop(work.top());
    if (rect.bottom() > work.bottom()) rect.moveBottom(work.bottom());
    if (rect.size() != m_window->size()) m_expectedProgrammaticSize = rect.size();
    m_window->setMinimumSize(QSize(qMin(PopupMinimum.width(), maximum.width()),
                                   qMin(PopupMinimum.height(), maximum.height())));
    m_window->setMaximumSize(maximum);
    applyGeometry(rect, screen);
}

void TrayPopup::applyGeometry(const QRect &rect, QScreen *screen)
{
#ifdef HEADROOM_LAYER_SHELL
    if (usesLayerShell()) {
        m_layerRect = rect;
        // Configure events are ordered, but compositors may coalesce or adjust
        // requested sizes. Keep enough recent targets to recover the matching
        // margin without retaining an unbounded drag history.
        while (m_pendingLayerRects.size() >= 32) m_pendingLayerRects.removeFirst();
        m_pendingLayerRects.append(rect);
        auto layer = LayerShellQt::Window::get(m_window);
        layer->setScreen(screen);
        layer->setDesiredSize(rect.size());
        const QRect full = screen->geometry();
        layer->setMargins(QMargins(rect.left() - full.left(), rect.top() - full.top(), 0, 0));
        syncLayerConfigure();
        return;
    }
#else
    Q_UNUSED(screen);
#endif
    m_window->setGeometry(rect);
}

bool TrayPopup::usesLayerShell() const
{
#ifdef HEADROOM_LAYER_SHELL
    return m_attached && QGuiApplication::platformName().startsWith("wayland");
#else
    return false;
#endif
}

QRect TrayPopup::effectiveGeometry() const
{
    if (usesLayerShell()) {
        if (m_layerRect.isValid()) return m_layerRect;
        if (m_configuredLayerRect.isValid()) return m_configuredLayerRect;
    }
    return m_window->geometry();
}

QPoint TrayPopup::resizePointer(const QMouseEvent *event) const
{
    if (usesLayerShell()) {
        const QRect configured = m_configuredLayerRect.isValid() ? m_configuredLayerRect : m_layerRect;
        if (configured.isValid()) return configured.topLeft() + event->position().toPoint();
    }
    return event->globalPosition().toPoint();
}

void TrayPopup::syncLayerConfigure()
{
    if (!usesLayerShell() || m_pendingLayerRects.isEmpty()) return;
    for (qsizetype index = m_pendingLayerRects.size() - 1; index >= 0; --index) {
        if (m_pendingLayerRects.at(index).size() != m_window->size()) continue;
        m_configuredLayerRect = m_pendingLayerRects.at(index);
        m_pendingLayerRects.erase(m_pendingLayerRects.begin(), m_pendingLayerRects.begin() + index + 1);
        return;
    }
}

void TrayPopup::writePendingSize()
{
    if (!m_sizeWriter || (!m_saveSize.isActive() && !m_resizing && !m_pendingUserSize.isValid())) return;
    m_saveSize.stop();
    m_preferred = m_pendingUserSize.isValid() ? m_pendingUserSize : effectiveGeometry().size();
    m_pendingUserSize = {};
    if (m_preferred.isValid()) m_sizeWriter(m_preferred);
}
void TrayPopup::show() {
    m_dismiss.stop(); position(); m_window->show(); m_window->raise(); m_window->requestActivate();
}
void TrayPopup::toggle(const QPoint &anchor, bool hasAnchor) {
    m_anchor = anchor; m_hasAnchor = hasAnchor;
    if (m_window->isVisible()) { m_dismiss.stop(); m_window->hide(); }
    else show();
}
