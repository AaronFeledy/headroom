#include "resetpolicy.h"
#include <QtTest>
#include "statemachine_assertions.h"
#include <cmath>
#include <limits>
#include <type_traits>
#include <utility>

// Pure policy tests over synthetic facts. They never construct a Controller,
// read usage, touch receipts, or perform a reset: a Submit outcome here is a
// decision value, not an executed request.
using namespace ResetPolicy;

Q_DECLARE_METATYPE(ResetPolicy::Provider)
Q_DECLARE_METATYPE(ResetPolicy::Window)
Q_DECLARE_METATYPE(ResetPolicy::Phase)
Q_DECLARE_METATYPE(ResetPolicy::Reason)

namespace QTest {
template <> inline char *toString(const Reason &reason) { return qstrdup(qPrintable(QString::number(int(reason)))); }
template <> inline char *toString(const Phase &phase) { return qstrdup(qPrintable(phaseName(phase))); }
}

namespace {
constexpr double unknownUsage = std::numeric_limits<double>::quiet_NaN();
// A fresh, bound, credited reading owned by an idle session. Every table
// starts here and changes one fact at a time.
Facts baseline(Provider provider, double utilization = 100) {
    Facts facts;
    facts.provider = provider; facts.readingAvailable = true; facts.fresh = true;
    facts.availableCount = 3; facts.accountBound = true; facts.window = Window::Future; facts.owner = true;
    facts.automationSupported = true;
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
// The desktop must not grow a local arm again: these facts stay compile-time
// absent, so a reintroduced automatic-spend path cannot hide behind them.
template <typename T, typename = void> struct HasLocalArm : std::false_type {};
template <typename T> struct HasLocalArm<T, std::void_t<
    decltype(std::declval<T>().armed), decltype(std::declval<T>().armKind),
    decltype(std::declval<T>().armWindow)>> : std::true_type {};
static_assert(!HasLocalArm<Facts>::value, "the desktop-owned automatic arm must not return");
Kind kind(const QString &id, const QString &label, qint64 count, bool usable, const QStringList &refills,
          bool givesBack = false) {
    Kind result;
    result.id = id; result.label = label; result.availableCount = count;
    result.usableNow = usable; result.refills = refills; result.givesBackWeekly = givesBack;
    return result;
}
// The example reading: one weekly reset plus two Fable resets.
Facts kinded(Facts facts) {
    facts.kindsKnown = true;
    facts.kinds = {kind("weekly", "weekly reset", 1, true, {"weekly", "weekly_fable"}),
        kind("fable", "Fable reset", 2, true, {"weekly_fable"}, true)};
    facts.buckets = {Bucket{"session", "5-Hour", 40.0}, Bucket{"weekly", "Weekly", 60.0},
        Bucket{"weekly_fable", "Fable", 27.0}};
    return facts;
}
}

class ResetPolicyTest : public QObject {
    Q_OBJECT
private slots:
    void init() {
        failOnStateMachineWarning();
    }
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
    void serverHeldArmStaysManageableWithoutFreshness() {
        for (const auto provider : {Provider::Codex, Provider::Claude}) {
            Facts facts = withUsable(baseline(provider, 96), 1);
            facts.automationArmed = true;
            auto result = evaluate(facts);
            QCOMPARE(result.phase, Phase::Armed);
            QVERIFY(result.visible && result.canOpen && result.canCancelArm);
            QCOMPARE(result.armReason, Reason::AlreadyArmed); QVERIFY(!result.canArm);
            // An older reading keeps it armed; no reading at all pauses it.
            facts.fresh = false;
            result = evaluate(facts);
            QCOMPARE(result.phase, Phase::Armed); QVERIFY(result.canCancelArm);
            QCOMPARE(result.useNowReason, Reason::Stale);
            facts.readingAvailable = false; facts.fresh = false; facts.availableCount.reset();
            facts.utilization.reset(); facts.accountBound = false;
            result = evaluate(facts);
            QCOMPARE(result.phase, Phase::Paused);
            QVERIFY(result.visible && result.canOpen && result.canCancelArm);
            QVERIFY(!result.canUseNow); QCOMPARE(result.useNowReason, Reason::Unavailable);
            // Use now may supersede a server-held arm while the reading permits it.
            Facts confirmedArm = confirmed(facts);
            confirmedArm.readingAvailable = true; confirmedArm.fresh = true;
            confirmedArm.availableCount = 3; confirmedArm.utilization = 96;
            confirmedArm.accountBound = true;
            result = evaluate(confirmedArm);
            QVERIFY(result.canConfirm); QVERIFY(!result.canSchedule);
            // A server arm belongs to its own provider, not the dialog slot.
            Facts other = withUsable(baseline(provider == Provider::Codex ? Provider::Claude : Provider::Codex, 96), 1);
            other.owner = false; other.automationArmed = true;
            result = evaluate(other);
            QCOMPARE(result.phase, Phase::Armed); QVERIFY(result.canOpen && result.canCancelArm);
            // A foreign dialog confirmation still blocks opening, never cancelling.
            Facts foreign = withUsable(baseline(provider, 96), 1);
            foreign.owner = false; foreign.confirmed = true; foreign.automationArmed = true;
            result = evaluate(foreign);
            QCOMPARE(result.openReason, Reason::ForeignOwner);
            QVERIFY(!result.canOpen && result.canCancelArm);
        }
    }
    void kindLabelsComposeFromServerText() {
        const Facts facts = kinded(baseline(Provider::Claude, 100));
        QCOMPARE(kindCountText(facts.kinds[0]), QString("1 weekly reset"));
        QCOMPARE(kindCountText(facts.kinds[1]), QString("2 Fable resets"));
        QCOMPARE(kindTitle(facts.kinds[0]), QString("Weekly reset"));
        QCOMPARE(kindTitle(facts.kinds[1]), QString("Fable reset"));
        QCOMPARE(kindRefillText(facts, facts.kinds[0]), QString("Refills Weekly and Fable"));
        QCOMPARE(kindRefillText(facts, facts.kinds[1]), QString("Refills Fable"));
        QCOMPARE(kindApplicabilityText(facts, facts.kinds[0]), QString("Used when Weekly or Fable reaches 100%"));
        QCOMPARE(kindApplicabilityText(facts, facts.kinds[1]), QString("Used when Weekly or Fable reaches 100%"));
        QCOMPARE(kindReturnText(facts, facts.kinds[1]), QString("Returns what Fable used to your weekly limit"));
        QVERIFY(kindReturnText(facts, facts.kinds[0]).isEmpty());
    }
    void kindGrantWordingIsComposedOnce() {
        const Facts facts = kinded(baseline(Provider::Claude, 100));
        Kind weekly = facts.kinds[0];
        const QString expires = QStringLiteral("Thu, Dec 10 at 4:00 PM");
        // The tooltip keeps its existing detail text, expiry included.
        QCOMPARE(kindDetailText(facts, weekly, QString()),
            QString("1 weekly reset · Refills Weekly and Fable"));
        QCOMPARE(kindDetailText(facts, weekly, expires),
            QString("1 weekly reset · Refills Weekly and Fable · Expires Thu, Dec 10 at 4:00 PM"));
        // Without a grant there is no wording to show.
        QVERIFY(kindNextText(weekly, expires).isEmpty());
        QVERIFY(kindUseNowText(weekly, expires).isEmpty());
        weekly.hasNext = true;
        weekly.nextLabel = QStringLiteral("One full limit reset (November 2026)");
        const QString quoted = QString(QChar(0x201C)) + weekly.nextLabel + QChar(0x201D);
        QCOMPARE(kindNextText(weekly, expires),
            QStringLiteral("Next: %1, expires %2").arg(quoted, expires));
        QCOMPARE(kindUseNowText(weekly, expires),
            QStringLiteral("Uses %1, which expires %2.").arg(quoted, expires));
        // An empty label falls back to our name for the kind; no expiry drops
        // the clause entirely.
        weekly.nextLabel.clear();
        QCOMPARE(kindNextText(weekly, QString()), QString("Next: one weekly reset"));
        QCOMPARE(kindUseNowText(weekly, QString()), QString("Uses one weekly reset."));
        QCOMPARE(kindUseNowText(weekly, expires),
            QString("Uses one weekly reset, which expires Thu, Dec 10 at 4:00 PM."));
    }
    void kindApplicabilityFollowsRefillsAndWeeklyGiveBack() {
        const Facts facts = kinded(baseline(Provider::Claude, 100));
        QVERIFY(kindApplies(facts, facts.kinds[0]));
        QVERIFY(kindApplies(facts, facts.kinds[1]));
        QCOMPARE(kindApplicabilityText(facts, facts.kinds[0]),
            QString("Used when Weekly or Fable reaches 100%"));
        QCOMPARE(kindApplicabilityText(facts, facts.kinds[1]),
            QString("Used when Weekly or Fable reaches 100%"));
        // A weekly give-back applies to the weekly meter even without a refill.
        Facts giveBack = kinded(baseline(Provider::Claude, 100));
        giveBack.kinds[1].refills = {"weekly_missing"};
        QVERIFY(kindApplies(giveBack, giveBack.kinds[1]));
        QCOMPARE(kindApplicabilityText(giveBack, giveBack.kinds[1]), QString("Used when Weekly reaches 100%"));
        Facts fableOnly = kinded(baseline(Provider::Claude, 100));
        fableOnly.kinds[1].givesBackWeekly = false;
        QCOMPARE(kindApplicabilityText(fableOnly, fableOnly.kinds[1]), QString("Used when Fable reaches 100%"));
        // A bucket that is not in the reading can never apply.
        Facts missing = kinded(baseline(Provider::Claude, 100));
        missing.kinds[1].refills = {"weekly_missing"};
        missing.kinds[1].givesBackWeekly = false;
        QVERIFY(!kindApplies(missing, missing.kinds[1]));
        QVERIFY(kindApplicabilityText(missing, missing.kinds[1]).isEmpty());
        // The session meter never triggers a kind that does not refill it.
        Facts session = kinded(baseline(Provider::Claude, 100));
        session.buckets.append(Bucket{"session_extra", "Session", 100.0});
        QVERIFY(kindApplies(session, session.kinds[0]));
        QCOMPARE(kindApplicabilityText(session, session.kinds[0]), QString("Used when Weekly or Fable reaches 100%"));
    }
    void kindAwareUseNowNeedsTheChosenKind() {
        Facts facts = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        auto result = evaluate(facts);
        QVERIFY(!result.canUseNow);
        QCOMPARE(result.useNowReason, Reason::KindRequired);
        facts.selectedKind = "weekly";
        result = evaluate(facts);
        QVERIFY(result.canUseNow);
        QCOMPARE(result.useNowReason, Reason::None);
        facts.selectedKind = "fable";
        facts.kinds[1].usableNow = false;
        result = evaluate(facts);
        QVERIFY(!result.canUseNow);
        QCOMPARE(result.useNowReason, Reason::ProviderRefused);
        facts.kinds[1].availableCount = 0;
        result = evaluate(facts);
        QCOMPARE(result.useNowReason, Reason::NoCredits);
        // Shared blockers still apply before the kind's own answer.
        Facts stale = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        stale.selectedKind = "weekly"; stale.fresh = false;
        QCOMPARE(evaluate(stale).useNowReason, Reason::Stale);
        // Legacy readings keep the top-level usable_now rule unchanged.
        Facts legacy = withUsable(baseline(Provider::Claude, 100), 1);
        QVERIFY(evaluate(legacy).canUseNow);
        QCOMPARE(evaluate(legacy).useNowReason, Reason::None);
    }
    void kindAwareArmingChoosesAnyOrOneKind() {
        Facts facts = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        auto result = evaluate(facts);
        // With several kinds and no specific choice, "Any matching reset" is
        // the default automatic choice; Use now still needs one named kind.
        QCOMPARE(facts.selectedKind, std::optional<QString>());
        QVERIFY(result.canArm);
        QCOMPARE(result.armReason, Reason::None);
        QCOMPARE(result.useNowReason, Reason::KindRequired);
        QCOMPARE(automaticKinds(facts), (QStringList{"weekly", "fable"}));
        QCOMPARE(automaticEffectText(facts),
            QString("The matching reset is used at whichever limit is reached."));
        // A specific kind limits the automatic choice to itself.
        facts.selectedKind = "fable";
        result = evaluate(facts);
        QVERIFY(result.canArm);
        QCOMPARE(automaticKinds(facts), QStringList{"fable"});
        QCOMPARE(automaticEffectText(facts), QString("Used when Weekly or Fable reaches 100%"));
        // A kind whose refills are absent from the reading cannot be armed.
        facts.kinds[1].givesBackWeekly = false;
        facts.buckets.removeIf([](const Bucket &bucket) { return bucket.id == "weekly_fable"; });
        result = evaluate(facts);
        QVERIFY(!result.canArm);
        QCOMPARE(result.armReason, Reason::NoTrigger);
        // With no credits in the chosen set there is nothing to authorize.
        Facts exhausted = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        exhausted.selectedKind = "fable";
        exhausted.kinds[1].availableCount = 0;
        QCOMPARE(evaluate(exhausted).armReason, Reason::NoCredits);
        // A zero-count kind is skipped by "Any matching reset".
        Facts partial = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        partial.kinds[1].availableCount = 0;
        QCOMPARE(automaticKinds(partial), QStringList{"weekly"});
        QVERIFY(evaluate(partial).canArm);
        // The future weekly window still gates arming.
        Facts ended = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        ended.selectedKind = "weekly"; ended.window = Window::Ended;
        QCOMPARE(evaluate(ended).armReason, Reason::WindowEnded);
    }
    void missingSelectionDisablesBothChoicesWithoutWidening() {
        Facts facts = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        facts.selectedKind = "fable";
        facts.selectedKindMissing = true;
        auto result = evaluate(facts);
        QVERIFY(result.visible && result.canOpen);
        QVERIFY(!result.canUseNow);
        QCOMPARE(result.useNowReason, Reason::KindMissing);
        QVERIFY(!result.canArm);
        QCOMPARE(result.armReason, Reason::KindMissing);
        QVERIFY(automaticKinds(facts).isEmpty());
        QVERIFY(automaticEffectText(facts).isEmpty());
        // It never widens to whatever kind happens to remain, one or several.
        facts.kinds.removeLast();
        result = evaluate(facts);
        QCOMPARE(result.useNowReason, Reason::KindMissing);
        QCOMPARE(result.armReason, Reason::KindMissing);
        QVERIFY(automaticKinds(facts).isEmpty());
        // ChatGPT facts and kindless Claude readings ignore the fact.
        Facts codex = baseline(Provider::Codex, 100);
        codex.selectedKind = "weekly"; codex.selectedKindMissing = true;
        result = evaluate(codex);
        QVERIFY(result.canUseNow && result.canArm);
        QVERIFY(automaticKinds(codex).isEmpty());
        Facts legacy = withUsable(baseline(Provider::Claude, 100), 1);
        legacy.selectedKindMissing = true;
        result = evaluate(legacy);
        QVERIFY(result.canUseNow && result.canArm);
        QCOMPARE(result.useNowReason, Reason::None);
        QVERIFY(automaticEffectText(legacy).isEmpty());
    }
    void armingRequiresAServerThatSupportsIt() {
        Facts facts = confirmed(withUsable(baseline(Provider::Claude, 100), 1));
        facts.automationSupported = false;
        const auto result = evaluate(facts);
        QVERIFY(result.visible);
        QVERIFY(!result.canArm && !result.canSchedule);
        QCOMPARE(result.armReason, Reason::ServerUnsupported);
        QCOMPARE(reasonText(result.armReason, facts.provider),
            QString("Update the server to use automatic resets."));
        // Use now is untouched by the server's automation support.
        QVERIFY(result.canUseNow && result.canConfirm);
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
            for (int reason = int(Reason::Unavailable); reason <= int(Reason::ServerUnsupported); ++reason)
                QVERIFY2(!reasonText(Reason(reason), provider).isEmpty(), qPrintable(QString::number(reason)));
            QVERIFY(!awaitingUsageText(provider, false).isEmpty());
            QVERIFY(!awaitingUsageText(provider, true).isEmpty());
        }
        // The blocker names the provider that holds the slot, not the one asking.
        QVERIFY(reasonText(Reason::ForeignOwner, Provider::Codex).contains("Claude"));
        QVERIFY(reasonText(Reason::ForeignOwner, Provider::Claude).contains("ChatGPT"));
    }
    void serverArmNeverOffersADesktopSpend() {
        // The server evaluates and spends its own arm. The policy's only
        // automatic capabilities are to offer one or cancel one.
        Facts facts = kinded(withUsable(baseline(Provider::Claude, 100), 1));
        facts.selectedKind = "weekly";
        facts.automationArmed = true; facts.automationKinds = {"weekly"};
        auto result = evaluate(facts);
        QCOMPARE(result.phase, Phase::Armed);
        QVERIFY(result.canCancelArm);
        QVERIFY(!result.canArm && !result.canSchedule);
        QCOMPARE(result.armReason, Reason::AlreadyArmed);
        // Use now remains an explicit, separately confirmed user action.
        QVERIFY(!result.canConfirm);
        result = evaluate(confirmed(facts));
        QVERIFY(result.canConfirm);
    }
};

QTEST_APPLESS_MAIN(ResetPolicyTest)
#include "test_resetpolicy.moc"
