#include "trayattention.h"
#include <QEvent>
#include <QGuiApplication>
#include <QStyleHints>
#include <QAccessibilityHints>
#include <utility>

void TrayAttentionState::update(const TrayVisual::Model &model, qint64 now, bool engaged) {
    if (m_provider != model.provider) acknowledge();
    m_provider = model.provider;
    const bool meter = model.kind == TrayVisual::Kind::Usage || model.kind == TrayVisual::Kind::Exhausted;
    if (!meter || model.provider.isEmpty()) {
        acknowledge();
        return;
    }
    // Both the main ring and secondary badge use the shared warning machine.
    const bool critical = model.level == Usage::WarningLevel::Critical
        || model.secondary == Usage::WarningLevel::Critical;
    const bool entered = critical && !m_critical.value(model.provider, false);
    // A snapshot may rename providers, so cap the episode map at the per-response
    // provider limit. Dropping stale names can only re-arm a provider that is no
    // longer reported; the current episode is already resolved above.
    if (m_critical.size() >= MaxTrackedProviders && !m_critical.contains(model.provider)) m_critical.clear();
    m_critical.insert(model.provider, critical);
    if (!critical || engaged) acknowledge();
    else if (entered) m_started = now;
}

void TrayAttentionState::acknowledge() { m_started = -1; }

bool TrayAttentionState::active(qint64 now) const {
    return m_started >= 0 && now >= m_started && now - m_started < FireMs + FlashMs;
}

int TrayAttentionState::interval(qint64 now) const {
    if (!active(now)) return 0;
    return now - m_started < FireMs ? 50 : 100;
}

TrayVisual::AttentionFrame TrayAttentionState::frame(qint64 now) const {
    if (!active(now)) return {};
    const qint64 elapsed = now - m_started;
    if (elapsed < FireMs) {
        // Quick ignition, a steady flicker, then a short fade back to the logo.
        const double envelope = qMin(1.0, qMin((elapsed + 50) / 200.0, (FireMs - elapsed) / 600.0));
        return {envelope, (elapsed % 800) / 800.0, false};
    }
    // Slow, high-contrast pulses; keep the normal meter visible between them.
    return {0, 0, (elapsed - FireMs) % 1500 < 500};
}

TrayAttention::TrayAttention(std::function<bool()> engaged, QObject *parent)
    : QObject(parent), m_engaged(std::move(engaged)) {
    m_clock.start();
    connect(&m_timer, &QTimer::timeout, this, &TrayAttention::tick);
}

bool TrayAttention::platformReducedMotion() {
    return QGuiApplication::styleHints()->accessibility()->motionPreference() == Qt::MotionPreference::ReducedMotion;
}

void TrayAttention::update(const TrayVisual::Model &model) {
    m_state.update(model, m_clock.elapsed(), m_engaged && m_engaged());
    tick();
}

void TrayAttention::acknowledge() {
    m_state.acknowledge();
    tick();
}

void TrayAttention::tick() {
    if (m_engaged && m_engaged()) m_state.acknowledge();
    const qint64 now = m_clock.elapsed();
    const auto next = m_state.frame(now);
    const int interval = m_state.interval(now);
    if (!interval) m_timer.stop();
    else if (!m_timer.isActive() || m_timer.interval() != interval) m_timer.start(interval);
    // Hover is checked while flashing, but icon updates only cross the tray
    // boundary when the visible frame changes. Nothing runs when idle.
    if (next.fire == m_frame.fire && next.phase == m_frame.phase && next.flash == m_frame.flash) return;
    m_frame = next;
    emit frameChanged();
}

bool TrayAttention::eventFilter(QObject *watched, QEvent *event) {
    switch (event->type()) {
    case QEvent::ToolTip:
    case QEvent::Enter:
    case QEvent::HoverEnter:
    case QEvent::Wheel:
    case QEvent::MouseButtonPress:
        acknowledge();
        break;
    default: break;
    }
    return QObject::eventFilter(watched, event);
}
