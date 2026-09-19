#include "notifications.h"
#include <algorithm>
#include <utility>

namespace {
void replaceTarget(QVariantList &items, const QVariantMap &event) {
    const auto target = event.value("target").toString();
    items.erase(std::remove_if(items.begin(), items.end(), [&](const QVariant &item) {
        return item.toMap().value("target").toString() == target;
    }), items.end());
    if (items.size() == Notifications::MaxPending) items.removeFirst();
    items.append(event);
}
}

void Notifications::post(const QString &target, const QString &title, const QString &message, int severity) {
    if (target.isEmpty()) return;
    replaceTarget(m_pending, {{"target", target}, {"title", title}, {"message", message}, {"severity", severity}});
    emit pendingChanged();
    emit desktopNotification(title, message, severity);
}

void Notifications::present() {
    if (m_pending.isEmpty()) return;
    for (const auto &event : std::as_const(m_pending)) {
        replaceTarget(m_presented, event.toMap());
        m_highlights.insert(event.toMap().value("target").toString());
    }
    // Bound presentation state too, even if events keep arriving while open.
    QSet<QString> retained;
    for (const auto &event : std::as_const(m_presented)) retained.insert(event.toMap().value("target").toString());
    m_highlights.intersect(retained);
    m_pending.clear();
    emit pendingChanged();
    emit presentationChanged();
}

void Notifications::endPresentation() {
    m_presented.clear(); m_highlights.clear();
    emit presentationChanged();
}

bool Notifications::claimHighlight(const QString &target) { return m_highlights.remove(target); }

void Notifications::observeUpdate(const QString &state, const QString &version, const QString &message, bool enabled) {
    if (state != "available" && state != "staged" && state != "failed") return;
    // Repeated checks of the same version do not re-arm a dismissed notification.
    const QString episode = version + (state == "failed" ? message : QString());
    if (m_updateEpisodes.contains(state) && m_updateEpisodes.value(state) == episode) return;
    m_updateEpisodes.insert(state, episode);
    if (!enabled) return;
    const QString title = state == "available" ? "Headroom update available"
        : state == "staged" ? "Headroom is ready to restart" : "Headroom update needs attention";
    post("desktopUpdate", title, message, state == "failed" ? 2 : 0);
}
