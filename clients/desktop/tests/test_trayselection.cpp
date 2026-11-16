#include "trayvisual.h"
#include "usage.h"
#include <QtTest>
#include "statemachine_assertions.h"

class TraySelectionTest : public QObject {
    Q_OBJECT
    const QDateTime now = QDateTime::fromString("2026-09-08T12:00:00Z", Qt::ISODate);
    QVariantMap provider(QString name, QVariantList buckets, bool success = true) const {
        return {{"provider_name", name}, {"is_success", success}, {"buckets", buckets}};
    }
    QVariantMap ready() const { return {{"status", "ready"}, {"lastGood", now.toSecsSinceEpoch()}, {"updated", "Just updated"}}; }
    static QVariantMap tiered(const QString &id, int tier, double used, int remaining = 9000) {
        return {{"id", id}, {"label", id == "session" ? "5-Hour" : "Weekly"}, {"utilization", used}, {"tier", tier},
            {"resets_at", QDateTime::fromString("2026-09-08T12:00:00Z", Qt::ISODate).addSecs(remaining).toString(Qt::ISODate)}};
    }
    static QVariantMap byTier(const QString &, const QVariantMap &bucket) { return {{"severity", bucket["tier"]}}; }
private slots:
    void init() {
        failOnStateMachineWarning();
    }
    void constrainedProviderRanksTierThenUsage() {
        const auto pick = [&](const QVariantList &rows, const QString &incumbent = {}) {
            return TrayVisual::constrainedProvider(rows, incumbent, byTier, now).key;
        };
        QCOMPARE(pick({provider("Claude", {tiered("weekly", 1, 90)}), provider("Codex", {tiered("weekly", 2, 40)})}), "Codex");
        QCOMPARE(pick({provider("Claude", {tiered("weekly", 2, 50)}), provider("Codex", {tiered("weekly", 2, 60)})}), "Codex");
        // The provider's most constrained meter counts, and equal readings keep the order.
        const auto claude = provider("Claude", {tiered("session", 0, 10), tiered("weekly", 3, 60)});
        QCOMPARE(pick({claude, provider("Codex", {tiered("weekly", 3, 60)})}), "Claude");
        QCOMPARE(TrayVisual::constrainedProvider({claude}, {}, byTier, now).meter, "weekly");
        auto work = provider("Claude (Work)", {tiered("weekly", 3, 70)});
        work["provider_type"] = "Claude"; work["account"] = QVariantMap{{"id", "work"}, {"label", "Work"}};
        QCOMPARE(pick({provider("Claude", {tiered("weekly", 1, 10)}), work}), "Claude:work");
    }
    void constrainedProviderHoldsCloseCompetitors() {
        const auto pick = [&](double challenger, int tier = 2, int heldTier = 2) {
            return TrayVisual::constrainedProvider({provider("Claude", {tiered("weekly", heldTier, 60)}),
                provider("Codex", {tiered("weekly", tier, challenger)})}, "Claude", byTier, now).key;
        };
        QCOMPARE(pick(64.9), "Claude");
        QCOMPARE(pick(65), "Codex");
        QCOMPARE(pick(10, 3), "Codex"); // A higher tier switches at once.
        QCOMPARE(pick(99, 2, 3), "Claude");
        // A held incumbent shows its own most constrained meter.
        const auto held = TrayVisual::constrainedProvider({provider("Claude", {tiered("weekly", 2, 60)}),
            provider("Codex", {tiered("session", 2, 64), tiered("weekly", 1, 90)})}, "Codex", byTier, now);
        QCOMPARE(held.key, "Codex");
        QCOMPARE(held.meter, "session");
    }
    void constrainedProviderRanksOnlyCurrentPercentReadings() {
        const auto pick = [&](const QVariantList &rows, const QString &incumbent) {
            return TrayVisual::constrainedProvider(rows, incumbent, byTier, now).key;
        };
        const auto codex = provider("Codex", {tiered("weekly", 0, 12)});
        // A failed incumbent yields even when it last read 100%.
        QCOMPARE(pick({provider("Claude", {tiered("weekly", 3, 100)}, false), codex}, "Claude"), "Codex");
        auto status = tiered("on_demand", 3, 100); status["measurement"] = "status";
        QCOMPARE(pick({provider("Cursor", {status}), codex}, "Cursor"), "Codex");
        // A passed window waits for its replacement instead of holding the tray.
        QCOMPARE(pick({provider("Claude", {tiered("session", 3, 100, -60)}), codex}, "Claude"), "Codex");
        QCOMPARE(pick({provider("Claude", {}, false), provider("Cursor", {status})}, "Claude"), QString());
    }
    void criticalStateCoversProvidersOffTheTray() {
        auto status = tiered("on_demand", 3, 100); status["measurement"] = "status";
        const auto model = TrayVisual::build(ready(), {provider("Claude", {tiered("weekly", 1, 40)}),
            provider("Codex", {tiered("session", 0, 10), tiered("weekly", 3, 99)}),
            provider("Grok", {tiered("weekly", 3, 99)}, false), provider("Cursor", {status})}, "Claude", {}, byTier, now);
        QCOMPARE(model.critical, (QHash<QString, bool>{{"Claude", false}, {"Codex", true}}));
        QCOMPARE(model.secondary, Usage::WarningLevel::Normal);
        QVERIFY(model.tooltip.startsWith("Claude · Weekly · 40% used"));
    }
    void ringShowsTheSelectedMeter() {
        const QVariantList rows{provider("Claude", {tiered("session", 0, 12), tiered("weekly", 3, 99)})};
        const auto model = TrayVisual::build(ready(), rows, "Claude", "weekly", byTier, now);
        QCOMPARE(model.used, 99);
        QCOMPARE(model.level, Usage::WarningLevel::Critical);
        QCOMPARE(model.secondary, Usage::WarningLevel::Normal);
        QVERIFY(model.tooltip.startsWith("Claude · Weekly · 99% used"));
        // A meter the provider no longer reports falls back to its first meter.
        const auto fallback = TrayVisual::build(ready(), rows, "Claude", "weekly_opus", byTier, now);
        QCOMPARE(fallback.used, 12);
        QCOMPARE(fallback.secondary, Usage::WarningLevel::Critical);
    }
};
QTEST_MAIN(TraySelectionTest)
#include "test_trayselection.moc"
