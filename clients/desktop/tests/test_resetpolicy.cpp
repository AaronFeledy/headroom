#include "resetpolicy.h"
#include <QtTest>
#include <cmath>
#include <limits>

// Pure policy tests over synthetic facts. They never construct a Controller,
// read usage, touch receipts, or perform a reset: a Submit outcome here is a
// decision value, not an executed request.
using namespace ResetPolicy;

Q_DECLARE_METATYPE(ResetPolicy::Provider)
Q_DECLARE_METATYPE(ResetPolicy::Window)
Q_DECLARE_METATYPE(ResetPolicy::Phase)
Q_DECLARE_METATYPE(ResetPolicy::Reason)
Q_DECLARE_METATYPE(ResetPolicy::ArmOutcome)

namespace QTest {
template <> inline char *toString(const Reason &reason) { return qstrdup(qPrintable(QString::number(int(reason)))); }
template <> inline char *toString(const Phase &phase) { return qstrdup(qPrintable(phaseName(phase))); }
template <> inline char *toString(const ArmOutcome &outcome) { return qstrdup(qPrintable(QString::number(int(outcome)))); }
}

namespace {
constexpr double unknownUsage = std::numeric_limits<double>::quiet_NaN();
// A fresh, bound, credited reading owned by an idle session. Every table
// starts here and changes one fact at a time.
Facts baseline(Provider provider, double utilization = 100) {
    Facts facts;
    facts.provider = provider; facts.readingAvailable = true; facts.fresh = true;
    facts.availableCount = 3; facts.accountBound = true; facts.window = Window::Future; facts.owner = true;
    if (std::isfinite(utilization)) facts.utilization = utilization;
    return facts;
}
Facts withUsable(Facts facts, int usable) {
    if (usable >= 0) facts.usableNow = usable == 1;
    return facts;
}
Facts confirmed(Facts facts, bool current = true) {
    facts.confirmed = true; facts.confirmationCurrent = current;
    return facts;
}
Facts armed(Facts facts) {
    facts.armed = true; facts.armIdentityCurrent = true; facts.armWindowCurrent = true; facts.armWindow = Window::Future;
    return facts;
}
}

class ResetPolicyTest : public QObject {
    Q_OBJECT
private slots:
    void codexFloorOffersAndPermitsFromNinety_data() {
        QTest::addColumn<double>("utilization");
        QTest::addColumn<bool>("offered");
        QTest::newRow("89.99") << 89.99 << false;
        QTest::newRow("90") << 90.0 << true;
        QTest::newRow("94.99") << 94.99 << true;
        QTest::newRow("95") << 95.0 << true;
        QTest::newRow("99.99") << 99.99 << true;
        QTest::newRow("100") << 100.0 << true;
        QTest::newRow("unknown") << unknownUsage << false;
    }
    void codexFloorOffersAndPermitsFromNinety() {
        QFETCH(double, utilization); QFETCH(bool, offered);
        const auto result = evaluate(baseline(Provider::Codex, utilization));
        QCOMPARE(result.visible, offered);
        QCOMPARE(result.phase, offered ? Phase::Choices : Phase::Hidden);
        QCOMPARE(result.canOpen, offered);
        // One floor: whatever is offered may also be used or armed right away.
        QCOMPARE(result.canUseNow, offered);
        QCOMPARE(result.canArm, offered);
        QCOMPARE(result.useNowReason, Reason::None);
        QCOMPARE(result.armReason, Reason::None);
        // Permission is never the same as a recorded confirmation.
        QVERIFY(!result.canConfirm); QVERIFY(!result.canSchedule); QVERIFY(!result.canCancelArm);
    }
    void claudeUsableNowGatesImmediateUseOnly_data() {
        QTest::addColumn<double>("utilization");
        QTest::addColumn<int>("usable");
        QTest::addColumn<bool>("visible");
        QTest::addColumn<bool>("canUseNow");
        QTest::addColumn<Reason>("useNowReason");
        QTest::newRow("89.99 null") << 89.99 << -1 << false << false << Reason::None;
        QTest::newRow("89.99 false") << 89.99 << 0 << false << false << Reason::None;
        QTest::newRow("89.99 true") << 89.99 << 1 << true << true << Reason::None;
        QTest::newRow("90 null") << 90.0 << -1 << true << false << Reason::ProviderUnknown;
        QTest::newRow("90 false") << 90.0 << 0 << true << false << Reason::ProviderRefused;
        QTest::newRow("90 true") << 90.0 << 1 << true << true << Reason::None;
        QTest::newRow("94.99 null") << 94.99 << -1 << true << false << Reason::ProviderUnknown;
        QTest::newRow("95 false") << 95.0 << 0 << true << false << Reason::ProviderRefused;
        QTest::newRow("100 null") << 100.0 << -1 << true << false << Reason::ProviderUnknown;
        QTest::newRow("100 false") << 100.0 << 0 << true << false << Reason::ProviderRefused;
        QTest::newRow("100 true") << 100.0 << 1 << true << true << Reason::None;
    }
    void claudeUsableNowGatesImmediateUseOnly() {
        QFETCH(double, utilization); QFETCH(int, usable); QFETCH(bool, visible);
        QFETCH(bool, canUseNow); QFETCH(Reason, useNowReason);
        const auto result = evaluate(withUsable(baseline(Provider::Claude, utilization), usable));
        QCOMPARE(result.visible, visible);
        QCOMPARE(result.canOpen, visible);
        QCOMPARE(result.canUseNow, canUseNow);
        // Arming follows the offer, not Anthropic's current answer.
        QCOMPARE(result.canArm, visible);
        QCOMPARE(result.useNowReason, useNowReason);
        QVERIFY(!result.canConfirm);
    }
    void sharedBlockersExplainBothChoices_data() {
        QTest::addColumn<Provider>("provider");
        QTest::addColumn<QString>("change");
        QTest::addColumn<bool>("visible");
        QTest::addColumn<Phase>("phase");
        QTest::addColumn<bool>("canOpen");
        QTest::addColumn<Reason>("reason");
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            const QByteArray name = provider == Provider::Claude ? "claude " : "codex ";
            QTest::newRow(name + "stale") << provider << "stale" << true << Phase::Choices << true << Reason::Stale;
            QTest::newRow(name + "unknown credits") << provider << "unknownCredits" << false << Phase::Hidden << false << Reason::NoCredits;
            QTest::newRow(name + "zero credits") << provider << "zeroCredits" << false << Phase::Hidden << false << Reason::NoCredits;
            QTest::newRow(name + "unbound") << provider << "unbound" << false << Phase::Hidden << false << Reason::Unbound;
            QTest::newRow(name + "receipt") << provider << "awaiting" << true << Phase::Reconciling << true << Reason::AwaitingUsage;
            QTest::newRow(name + "offline") << provider << "offline" << false << Phase::Hidden << false << Reason::Unavailable;
            QTest::newRow(name + "receipt offline") << provider << "awaitingOffline" << true << Phase::Reconciling << true << Reason::AwaitingUsage;
            QTest::newRow(name + "receipt unknown account") << provider << "awaitingUnbound" << true << Phase::Reconciling << true << Reason::AwaitingUsage;
            QTest::newRow(name + "busy owner") << provider << "busy" << true << Phase::Submitting << false << Reason::Busy;
            QTest::newRow(name + "foreign arm") << provider << "foreignArm" << true << Phase::Choices << false << Reason::ForeignOwner;
            QTest::newRow(name + "foreign confirmation") << provider << "foreignConfirmation" << true << Phase::Choices << false << Reason::ForeignOwner;
            QTest::newRow(name + "foreign submission") << provider << "foreignBusy" << true << Phase::Choices << false << Reason::ForeignOwner;
        }
    }
    void sharedBlockersExplainBothChoices() {
        QFETCH(Provider, provider); QFETCH(QString, change); QFETCH(bool, visible);
        QFETCH(Phase, phase); QFETCH(bool, canOpen); QFETCH(Reason, reason);
        Facts facts = withUsable(baseline(provider), 1);
        if (change == "stale") facts.fresh = false;
        else if (change == "unknownCredits") facts.availableCount.reset();
        else if (change == "zeroCredits") facts.availableCount = 0;
        else if (change == "unbound") facts.accountBound = false;
        else if (change == "awaiting") facts.awaitingUsage = true;
        else if (change == "awaitingUnbound") {
            facts.accountBound = false; facts.availableCount.reset(); facts.awaitingUsage = true;
        }
        else if (change == "offline" || change == "awaitingOffline") {
            facts.readingAvailable = false; facts.fresh = false; facts.availableCount.reset(); facts.utilization.reset();
            facts.usableNow.reset(); facts.accountBound = false; facts.window = Window::Unknown;
            facts.awaitingUsage = change == "awaitingOffline";
        }
        else if (change == "busy") facts.busy = true;
        else if (change == "foreignArm") { facts.owner = false; facts.armed = true; }
        else if (change == "foreignConfirmation") { facts.owner = false; facts.confirmed = true; }
        else if (change == "foreignBusy") { facts.owner = false; facts.busy = true; }
        const auto result = evaluate(facts);
        QCOMPARE(result.visible, visible);
        QCOMPARE(result.phase, phase);
        QCOMPARE(result.canOpen, canOpen);
        QVERIFY(!result.canUseNow); QVERIFY(!result.canArm); QVERIFY(!result.canConfirm); QVERIFY(!result.canSchedule);
        if (!visible) return;
        QCOMPARE(result.useNowReason, reason);
        QCOMPARE(result.armReason, reason);
        if (!canOpen) QCOMPARE(result.openReason, reason);
        QVERIFY(!reasonText(result.useNowReason, provider).isEmpty());
    }
    void confirmationIdentityBindsActualUse() {
        const Facts permitted = withUsable(baseline(Provider::Claude), 1);
        auto result = evaluate(permitted);
        QVERIFY(result.canUseNow && result.canArm);
        QVERIFY(!result.canConfirm && !result.canSchedule);
        QCOMPARE(result.useNowReason, Reason::None);
        result = evaluate(confirmed(permitted));
        QVERIFY(result.canConfirm && result.canSchedule);
        // The account or connection changed after the confirmation was recorded.
        result = evaluate(confirmed(permitted, false));
        QVERIFY(result.canUseNow && result.canArm);
        QVERIFY(!result.canConfirm && !result.canSchedule);
        QCOMPARE(result.useNowReason, Reason::ConfirmationStale);
        QCOMPARE(result.armReason, Reason::ConfirmationStale);
        // A confirmation never upgrades a reading that forbids use.
        result = evaluate(confirmed(withUsable(baseline(Provider::Claude), 0)));
        QVERIFY(!result.canConfirm); QVERIFY(result.canSchedule);
        QCOMPARE(result.useNowReason, Reason::ProviderRefused);
        // An owned confirmation keeps the options visible after usage drops.
        result = evaluate(confirmed(baseline(Provider::Codex, 85)));
        QVERIFY(result.visible); QCOMPARE(result.phase, Phase::Choices);
        QVERIFY(!result.canConfirm); QCOMPARE(result.useNowReason, Reason::BelowFloor);
    }
    void armingNeedsKnownFutureWindow_data() {
        QTest::addColumn<Provider>("provider");
        QTest::addColumn<Window>("window");
        QTest::addColumn<Reason>("armReason");
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            const QByteArray name = provider == Provider::Claude ? "claude " : "codex ";
            QTest::newRow(name + "future") << provider << Window::Future << Reason::None;
            QTest::newRow(name + "unknown") << provider << Window::Unknown << Reason::WindowUnknown;
            QTest::newRow(name + "ended") << provider << Window::Ended << Reason::WindowEnded;
        }
    }
    void armingNeedsKnownFutureWindow() {
        QFETCH(Provider, provider); QFETCH(Window, window); QFETCH(Reason, armReason);
        Facts facts = confirmed(withUsable(baseline(provider), 1));
        facts.window = window;
        const auto result = evaluate(facts);
        QCOMPARE(result.armReason, armReason);
        QCOMPARE(result.canArm, armReason == Reason::None);
        QCOMPARE(result.canSchedule, armReason == Reason::None);
        // The window gates arming only; immediate use stays permitted.
        QVERIFY(result.canUseNow && result.canConfirm);
    }
    void ownedArmStaysManageableWithoutFreshness() {
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            auto result = evaluate(armed(baseline(provider, 96)));
            QCOMPARE(result.phase, Phase::Armed);
            QVERIFY(result.visible && result.canOpen && result.canCancelArm);
            QCOMPARE(result.armReason, Reason::AlreadyArmed); QVERIFY(!result.canArm);
            // An older reading keeps monitoring; no reading at all pauses it.
            Facts aged = armed(baseline(provider, 96)); aged.fresh = false;
            result = evaluate(aged);
            QCOMPARE(result.phase, Phase::Armed); QVERIFY(result.canCancelArm);
            QCOMPARE(result.useNowReason, Reason::Stale);
            Facts paused = armed(baseline(provider, 96));
            paused.readingAvailable = false; paused.fresh = false; paused.availableCount.reset();
            paused.utilization.reset(); paused.accountBound = false;
            result = evaluate(paused);
            QCOMPARE(result.phase, Phase::Paused);
            QVERIFY(result.visible && result.canOpen && result.canCancelArm);
            QVERIFY(!result.canUseNow); QCOMPARE(result.useNowReason, Reason::Unavailable);
            // Use now may supersede an arm while the reading permits it.
            result = evaluate(confirmed(armed(withUsable(baseline(provider, 96), 1))));
            QVERIFY(result.canConfirm); QVERIFY(!result.canSchedule);
            // Ownership is exclusive: the other provider sees its own offer but cannot open.
            Facts other = withUsable(baseline(provider == Provider::Codex ? Provider::Claude : Provider::Codex, 96), 1);
            other.owner = false; other.armed = true;
            result = evaluate(other);
            QCOMPARE(result.phase, Phase::Choices); QVERIFY(!result.canOpen && !result.canCancelArm);
            QCOMPARE(result.openReason, Reason::ForeignOwner);
        }
    }
    void armObservationDecisions_data() {
        QTest::addColumn<Provider>("provider");
        QTest::addColumn<double>("utilization");
        QTest::addColumn<int>("usable");
        QTest::addColumn<QStringList>("changes");
        QTest::addColumn<ArmOutcome>("outcome");
        QTest::addColumn<Reason>("reason");
        QTest::newRow("codex 89.99 cancels") << Provider::Codex << 89.99 << -1 << QStringList{} << ArmOutcome::Cancel << Reason::BelowFloor;
        QTest::newRow("codex 90 keeps") << Provider::Codex << 90.0 << -1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex 94.99 keeps") << Provider::Codex << 94.99 << -1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex 95 keeps") << Provider::Codex << 95.0 << -1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex 99.99 keeps") << Provider::Codex << 99.99 << -1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex 100 submits") << Provider::Codex << 100.0 << -1 << QStringList{} << ArmOutcome::Submit << Reason::None;
        QTest::newRow("codex 100 stale keeps") << Provider::Codex << 100.0 << -1 << QStringList{"stale"} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex 100 no credits") << Provider::Codex << 100.0 << -1 << QStringList{"zeroCredits"} << ArmOutcome::Cancel << Reason::NoCredits;
        QTest::newRow("codex 100 unknown credits") << Provider::Codex << 100.0 << -1 << QStringList{"unknownCredits"} << ArmOutcome::Cancel << Reason::NoCredits;
        QTest::newRow("codex 100 receipt") << Provider::Codex << 100.0 << -1 << QStringList{"awaiting"} << ArmOutcome::Cancel << Reason::AwaitingUsage;
        QTest::newRow("codex identity changed") << Provider::Codex << 100.0 << -1 << QStringList{"identity"} << ArmOutcome::Cancel << Reason::IdentityChanged;
        QTest::newRow("codex window changed") << Provider::Codex << 100.0 << -1 << QStringList{"window"} << ArmOutcome::Cancel << Reason::IdentityChanged;
        QTest::newRow("codex unknown usage waits") << Provider::Codex << unknownUsage << -1 << QStringList{} << ArmOutcome::Wait << Reason::None;
        QTest::newRow("codex provider error waits") << Provider::Codex << 100.0 << -1 << QStringList{"unavailable"} << ArmOutcome::Wait << Reason::None;
        QTest::newRow("codex deadline ended offline") << Provider::Codex << 100.0 << -1 << QStringList{"unavailable", "ended"} << ArmOutcome::Cancel << Reason::WindowEnded;
        QTest::newRow("codex deadline unknown") << Provider::Codex << 100.0 << -1 << QStringList{"deadlineUnknown"} << ArmOutcome::Cancel << Reason::WindowUnknown;
        QTest::newRow("codex busy never repeats") << Provider::Codex << 100.0 << -1 << QStringList{"busy"} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("codex unarmed") << Provider::Codex << 100.0 << -1 << QStringList{"unarmed"} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("claude 40 refused keeps") << Provider::Claude << 40.0 << 0 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("claude 100 refused keeps") << Provider::Claude << 100.0 << 0 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("claude 100 unknown keeps") << Provider::Claude << 100.0 << -1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("claude 99.99 usable keeps") << Provider::Claude << 99.99 << 1 << QStringList{} << ArmOutcome::Keep << Reason::None;
        QTest::newRow("claude 100 usable submits") << Provider::Claude << 100.0 << 1 << QStringList{} << ArmOutcome::Submit << Reason::None;
        QTest::newRow("claude 100 usable receipt") << Provider::Claude << 100.0 << 1 << QStringList{"awaiting"} << ArmOutcome::Cancel << Reason::AwaitingUsage;
        QTest::newRow("claude 100 no credits") << Provider::Claude << 100.0 << 1 << QStringList{"zeroCredits"} << ArmOutcome::Cancel << Reason::NoCredits;
        QTest::newRow("claude deadline ended offline") << Provider::Claude << 100.0 << 1 << QStringList{"unavailable", "ended"} << ArmOutcome::Cancel << Reason::WindowEnded;
    }
    void armObservationDecisions() {
        QFETCH(Provider, provider); QFETCH(double, utilization); QFETCH(int, usable);
        QFETCH(QStringList, changes); QFETCH(ArmOutcome, outcome); QFETCH(Reason, reason);
        Facts facts = armed(withUsable(baseline(provider, utilization), usable));
        for (const QString &change : changes) {
            if (change == "stale") facts.fresh = false;
            else if (change == "zeroCredits") facts.availableCount = 0;
            else if (change == "unknownCredits") facts.availableCount.reset();
            else if (change == "awaiting") facts.awaitingUsage = true;
            else if (change == "identity") facts.armIdentityCurrent = false;
            else if (change == "window") facts.armWindowCurrent = false;
            else if (change == "unavailable") { facts.readingAvailable = false; facts.fresh = false; }
            else if (change == "ended") facts.armWindow = Window::Ended;
            else if (change == "deadlineUnknown") facts.armWindow = Window::Unknown;
            else if (change == "busy") facts.busy = true;
            else if (change == "unarmed") facts.armed = false;
            else QFAIL(qPrintable("unknown change " + change));
        }
        const auto decision = observeArm(facts);
        QCOMPARE(decision.outcome, outcome);
        QCOMPARE(decision.reason, reason);
        if (outcome == ArmOutcome::Cancel) QVERIFY(!cancellationText(reason).isEmpty());
    }
    void missingArmBindingDistinguishesSuccessFromFailure_data() {
        QTest::addColumn<Provider>("provider");
        QTest::addColumn<bool>("succeeded");
        QTest::addColumn<bool>("missingAccount");
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            const QByteArray name = provider == Provider::Claude ? "claude " : "codex ";
            QTest::newRow(name + "success missing account and window") << provider << true << true;
            QTest::newRow(name + "failure missing account and window") << provider << false << true;
            QTest::newRow(name + "success missing window") << provider << true << false;
            QTest::newRow(name + "failure missing window") << provider << false << false;
        }
    }
    void missingArmBindingDistinguishesSuccessFromFailure() {
        QFETCH(Provider, provider); QFETCH(bool, succeeded); QFETCH(bool, missingAccount);
        Facts facts = armed(baseline(provider, unknownUsage));
        facts.readingAvailable = succeeded;
        facts.availableCount.reset(); facts.usableNow.reset();
        facts.accountBound = !missingAccount; facts.armIdentityCurrent = !missingAccount;
        facts.window = Window::Unknown; facts.armWindowCurrent = false;
        const auto decision = observeArm(facts);
        QCOMPARE(decision.outcome, succeeded ? ArmOutcome::Cancel : ArmOutcome::Wait);
        QCOMPARE(decision.reason, succeeded ? Reason::IdentityChanged : Reason::None);
    }
    void armExpiryFollowsRecordedDeadline() {
        QCOMPARE(armExpiry(Window::Future), Reason::None);
        QCOMPARE(armExpiry(Window::Ended), Reason::WindowEnded);
        QCOMPARE(armExpiry(Window::Unknown), Reason::WindowUnknown);
    }
    void codexReceiptReleasesOnlyBelowFloor_data() {
        QTest::addColumn<double>("utilization");
        QTest::addColumn<bool>("released");
        QTest::newRow("0") << 0.0 << true;
        QTest::newRow("89.99") << 89.99 << true;
        QTest::newRow("90") << 90.0 << false;
        QTest::newRow("94.99") << 94.99 << false;
        QTest::newRow("95") << 95.0 << false;
        QTest::newRow("100") << 100.0 << false;
        QTest::newRow("negative") << -1.0 << false;
        QTest::newRow("unknown") << unknownUsage << false;
        QTest::newRow("infinite") << std::numeric_limits<double>::infinity() << false;
    }
    void codexReceiptReleasesOnlyBelowFloor() {
        QFETCH(double, utilization); QFETCH(bool, released);
        QCOMPARE(releasesCodexReceipt(std::isnan(utilization) ? std::nullopt : std::optional<double>(utilization)), released);
        QVERIFY(!releasesCodexReceipt(std::nullopt));
    }
    void copyNamesEveryStateAndReason() {
        for (const int phase : {0, 1, 2, 3, 4, 5}) QVERIFY(!phaseName(Phase(phase)).isEmpty());
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            QVERIFY(reasonText(Reason::None, provider).isEmpty());
            for (int reason = int(Reason::Unavailable); reason <= int(Reason::IdentityChanged); ++reason)
                QVERIFY2(!reasonText(Reason(reason), provider).isEmpty(), qPrintable(QString::number(reason)));
            QVERIFY(!awaitingUsageText(provider, false).isEmpty());
            QVERIFY(!awaitingUsageText(provider, true).isEmpty());
        }
        // The blocker names the provider that holds the slot, not the one asking.
        QVERIFY(reasonText(Reason::ForeignOwner, Provider::Codex).contains("Claude"));
        QVERIFY(reasonText(Reason::ForeignOwner, Provider::Claude).contains("ChatGPT"));
        QVERIFY(!cancellationText(Reason::None).isEmpty());
    }
};

QTEST_APPLESS_MAIN(ResetPolicyTest)
#include "test_resetpolicy.moc"
