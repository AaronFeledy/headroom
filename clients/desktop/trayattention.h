#pragma once
#include "trayvisual.h"
#include <QElapsedTimer>
#include <QHash>
#include <QObject>
#include <QTimer>

// Acknowledgement belongs to a critical episode, not a poll or animation frame.
// Missing data suspends attention without claiming the provider has recovered.
class TrayAttentionState {
public:
    static constexpr qint64 FireMs = 4000;
    static constexpr qint64 FlashMs = 180000;
    // Usage::parse accepts at most 64 providers in one response.
    static constexpr int MaxTrackedProviders = 64;
    void update(const TrayVisual::Model &model, qint64 now, bool engaged = false);
    void acknowledge();
    TrayVisual::AttentionFrame frame(qint64 now) const;
    bool active(qint64 now) const;
    int interval(qint64 now) const;
private:
    QHash<QString, bool> m_critical;
    QString m_provider;
    qint64 m_started = -1;
};

class TrayAttention : public QObject {
    Q_OBJECT
public:
    explicit TrayAttention(std::function<bool()> engaged = {}, QObject *parent = nullptr);
    static bool platformReducedMotion();
    void update(const TrayVisual::Model &model);
    void acknowledge();
    TrayVisual::AttentionFrame frame() const { return m_frame; }
signals:
    void frameChanged();
protected:
    bool eventFilter(QObject *watched, QEvent *event) override;
private:
    void tick();
    TrayAttentionState m_state;
    TrayVisual::AttentionFrame m_frame;
    QElapsedTimer m_clock;
    QTimer m_timer;
    std::function<bool()> m_engaged;
};
