#include "trayattention.h"
#include <QEvent>
#include <QtTest>
#include "statemachine_assertions.h"

class TrayAttentionTest : public QObject {
    Q_OBJECT
    TrayVisual::Model model(Usage::WarningLevel level = Usage::WarningLevel::Critical) const {
        TrayVisual::Model result;
        result.provider = "Claude";
        result.kind = TrayVisual::Kind::Usage;
        result.used = 12; // Deliberately low: severity comes from the shared FSM.
        result.level = level;
        return result;
    }
private slots:
    void init() {
        failOnStateMachineWarning();
    }
    void fireThenBoundedSlowFlashing() {
        TrayAttentionState state;
        state.update(model(), 100);
        QVERIFY(state.frame(100).fire > 0);
        QCOMPARE(state.interval(100), 50);
        QCOMPARE(state.frame(100 + 200).fire, 1.0);
        QVERIFY(state.frame(100 + 3900).fire < 1);
        QCOMPARE(state.frame(100 + 4000).fire, 0.0);
        QVERIFY(state.frame(100 + 4000).flash);
        QVERIFY(!state.frame(100 + 4500).flash);
        QVERIFY(state.frame(100 + 5500).flash);
        QCOMPARE(state.interval(100 + 4000), 100);
        QVERIFY(state.active(100 + TrayAttentionState::FireMs + TrayAttentionState::FlashMs - 1));
        const qint64 end = 100 + TrayAttentionState::FireMs + TrayAttentionState::FlashMs;
        QVERIFY(!state.active(end));
        QCOMPARE(state.interval(end), 0);
        QVERIFY(!state.frame(end).flash);
        state.update(model(), end + 1000);
        QVERIFY(!state.active(end + 1000));
    }
    void providerHistoryStaysBoundedWithoutDisturbingTheCurrentEpisode() {
        // A compatible backend may report a different primary provider on every
        // poll. Tracking must stay bounded while the live episode is preserved.
        TrayAttentionState state;
        TrayVisual::Model renamed = model();
        for (int i = 0; i < TrayAttentionState::MaxTrackedProviders * 4; ++i) {
            renamed.provider = QStringLiteral("Provider-%1").arg(i);
            state.update(renamed, i);
            QVERIFY(state.active(i));
        }
        // The current provider keeps its acknowledgement instead of re-arming.
        TrayVisual::Model stable = model();
        state.update(stable, 100000);
        QVERIFY(state.active(100000));
        state.acknowledge();
        state.update(stable, 100100);
        QVERIFY(!state.active(100100));
    }
    void pollingAndAcknowledgementDoNotRestartTheEpisode() {
        TrayAttentionState state;
        state.update(model(), 0);
        state.update(model(), 5000);
        QCOMPARE(state.frame(5000).fire, 0.0);
        state.acknowledge();
        QVERIFY(!state.active(5001));
        state.update(model(), 6000);
        QVERIFY(!state.active(6000));
        state.update(model(Usage::WarningLevel::Warning), 7000);
        state.update(model(), 8000);
        QVERIFY(state.active(8000));
        QVERIFY(state.frame(8000).fire > 0);
    }
    void activeDashboardAcknowledgesNewAndExistingEpisodes() {
        TrayAttentionState state;
        state.update(model(), 0, true);
        QVERIFY(!state.active(0));
        state.update(model(), 1000, false);
        QVERIFY(!state.active(1000));
        state.update(model(Usage::WarningLevel::Normal), 2000);
        state.update(model(), 3000);
        QVERIFY(state.active(3000));
        state.update(model(), 3500, true);
        QVERIFY(!state.active(3500));
    }
    void recoveryAndMissingDataStopWithoutRepeatedAlarms_data() {
        QTest::addColumn<int>("kind");
        using K = TrayVisual::Kind;
        for (const auto kind : {K::Offline, K::AuthError, K::ApiError, K::Malformed,
                               K::ProviderError, K::Connecting, K::Idle, K::Setup})
            QTest::newRow(qPrintable(QString::number(int(kind)))) << int(kind);
    }
    void recoveryAndMissingDataStopWithoutRepeatedAlarms() {
        QFETCH(int, kind);
        TrayAttentionState state;
        state.update(model(), 0);
        auto missing = model(); missing.kind = TrayVisual::Kind(kind);
        state.update(missing, 1000);
        QVERIFY(!state.active(1000));
        state.update(model(), 2000);
        QVERIFY(!state.active(2000));
        state.update(model(Usage::WarningLevel::Warning), 3000);
        state.update(model(), 4000);
        QVERIFY(state.active(4000));
        state.update(model(Usage::WarningLevel::Warning), 4500);
        QVERIFY(!state.active(4500));
    }
    void secondaryCriticalAndProviderSelection() {
        TrayAttentionState state;
        auto secondary = model(Usage::WarningLevel::Normal);
        secondary.secondary = Usage::WarningLevel::Critical;
        state.update(secondary, 0);
        QVERIFY(state.active(0));
        auto other = model(Usage::WarningLevel::Warning); other.provider = "Codex";
        state.update(other, 1000);
        QVERIFY(!state.active(1000));
        state.update(secondary, 2000);
        QVERIFY(!state.active(2000));
        other.kind = TrayVisual::Kind::Exhausted; other.level = Usage::WarningLevel::Critical;
        state.update(other, 3000);
        QVERIFY(state.active(3000));
    }
    void hoverAndOtherTrayEventsAcknowledgeWithoutSwallowingThem_data() {
        QTest::addColumn<int>("eventType");
        for (const auto type : {QEvent::ToolTip, QEvent::Enter, QEvent::HoverEnter, QEvent::Wheel, QEvent::MouseButtonPress})
            QTest::newRow(qPrintable(QString::number(type))) << int(type);
    }
    void hoverAndOtherTrayEventsAcknowledgeWithoutSwallowingThem() {
        QFETCH(int, eventType);
        class Receiver : public QObject {
        public:
            bool received = false;
            bool event(QEvent *) override { received = true; return true; }
        } receiver;
        TrayAttention attention;
        receiver.installEventFilter(&attention);
        attention.update(model());
        QVERIFY(attention.frame().fire > 0);
        QSignalSpy changed(&attention, &TrayAttention::frameChanged);
        QEvent event{QEvent::Type(eventType)};
        QCoreApplication::sendEvent(&receiver, &event);
        QVERIFY(receiver.received);
        QCOMPARE(attention.frame().fire, 0.0);
        QCOMPARE(changed.count(), 1);
        attention.update(model());
        QCOMPARE(attention.frame().fire, 0.0);
    }
};
QTEST_MAIN(TrayAttentionTest)
#include "test_trayattention.moc"
