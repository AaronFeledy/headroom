#include "resetpolicy.h"
#include <cmath>

namespace {
// Both providers offer their options from this usage; Claude's usable_now
// also offers them below it.
constexpr double offerFloor = 90;
// A ChatGPT reset may be used, kept armed, or kept latched from this usage.
// OpenAI's own help only requires a covered allowance below 80% remaining;
// this is a conservative Headroom threshold, not a provider limit.
constexpr double codexManualFloor = 90;
// Automatic use still waits for a reading at the limit.
constexpr double usageLimit = 100;

bool known(std::optional<double> value) { return value && std::isfinite(*value) && *value >= 0; }
}

namespace ResetPolicy {

Capabilities evaluate(const Facts &facts) {
    const bool ownedBusy = facts.owner && facts.busy;
    const bool ownedArmed = facts.owner && facts.armed;
    const bool ownedConfirmed = facts.owner && facts.confirmed;
    const bool foreign = !facts.owner && (facts.busy || facts.confirmed || facts.armed);
    const bool credits = facts.availableCount && *facts.availableCount > 0;
    const bool atOfferFloor = known(facts.utilization) && *facts.utilization >= offerFloor;
    const bool atCodexFloor = known(facts.utilization) && *facts.utilization >= codexManualFloor;
    const bool claudeUsable = facts.provider == Provider::Claude && facts.usableNow && *facts.usableNow;
    const bool offered = credits && facts.accountBound && (atOfferFloor || claudeUsable);
    Capabilities result;
    // Owned context stays visible even when the current reading is unavailable.
    result.visible = ownedBusy || ownedArmed || ownedConfirmed || facts.awaitingUsage || offered;
    if (!result.visible) return result;
    // An arm pauses while the provider or backend reports no reading; a
    // reading that is merely older than the freshness window keeps monitoring.
    result.phase = ownedBusy ? Phase::Submitting
        : ownedArmed ? (facts.readingAvailable ? Phase::Armed : Phase::Paused)
        : facts.awaitingUsage ? Phase::Reconciling : Phase::Choices;
    result.openReason = foreign ? Reason::ForeignOwner : facts.busy ? Reason::Busy : Reason::None;
    result.canOpen = result.openReason == Reason::None;
    result.canCancelArm = ownedArmed;
    // Blockers shared by both choices, most specific first. Visibility is
    // never permission: a stale or latched reading opens for information only.
    const Reason shared = foreign ? Reason::ForeignOwner : facts.busy ? Reason::Busy
        : facts.awaitingUsage ? Reason::AwaitingUsage : !facts.readingAvailable ? Reason::Unavailable
        : !facts.fresh ? Reason::Stale : !credits ? Reason::NoCredits
        : !facts.accountBound ? Reason::Unbound : Reason::None;
    if (shared != Reason::None) result.useNowReason = shared;
    else if (facts.provider == Provider::Codex) result.useNowReason = atCodexFloor ? Reason::None : Reason::BelowFloor;
    else result.useNowReason = claudeUsable ? Reason::None
        : facts.usableNow ? Reason::ProviderRefused : Reason::ProviderUnknown;
    // Arming is independent of immediate permission: Claude may arm without
    // usable_now, which the firing reading must still report.
    result.armReason = ownedArmed ? Reason::AlreadyArmed : shared != Reason::None ? shared
        : !offered ? Reason::BelowFloor : facts.window == Window::Future ? Reason::None
        : facts.window == Window::Ended ? Reason::WindowEnded : Reason::WindowUnknown;
    const bool viewBound = ownedConfirmed && facts.confirmationCurrent;
    result.canUseNow = result.useNowReason == Reason::None;
    result.canArm = result.armReason == Reason::None;
    result.canConfirm = result.canUseNow && viewBound;
    result.canSchedule = result.canArm && viewBound;
    if (ownedConfirmed && !facts.confirmationCurrent) {
        if (result.canUseNow) result.useNowReason = Reason::ConfirmationStale;
        if (result.canArm) result.armReason = Reason::ConfirmationStale;
    }
    return result;
}

ArmDecision observeArm(const Facts &facts) {
    if (!(facts.owner && facts.armed) || facts.busy) return {};
    if (const Reason expiry = armExpiry(facts.armWindow); expiry != Reason::None) return {ArmOutcome::Cancel, expiry};
    // A temporary provider error pauses monitoring; a successful reading with a
    // different identity or window invalidates the authorization.
    if (!facts.readingAvailable) return {ArmOutcome::Wait, Reason::None};
    if (!facts.armIdentityCurrent || !facts.armWindowCurrent) return {ArmOutcome::Cancel, Reason::IdentityChanged};
    if (!known(facts.utilization)) return {ArmOutcome::Wait, Reason::None};
    if (facts.provider == Provider::Codex && *facts.utilization < codexManualFloor) return {ArmOutcome::Cancel, Reason::BelowFloor};
    if (!facts.availableCount || *facts.availableCount <= 0) return {ArmOutcome::Cancel, Reason::NoCredits};
    if (facts.awaitingUsage) return {ArmOutcome::Cancel, Reason::AwaitingUsage};
    // Claude keeps waiting below the limit or without usable_now; it never
    // cancels for usage alone.
    if (*facts.utilization >= usageLimit && evaluate(facts).canUseNow) return {ArmOutcome::Submit, Reason::None};
    return {};
}

Reason armExpiry(Window armWindow) {
    return armWindow == Window::Future ? Reason::None
        : armWindow == Window::Ended ? Reason::WindowEnded : Reason::WindowUnknown;
}

bool releasesCodexReceipt(std::optional<double> utilization) {
    return known(utilization) && *utilization < codexManualFloor;
}

QString phaseName(Phase phase) {
    switch (phase) {
    case Phase::Hidden: return QStringLiteral("hidden");
    case Phase::Choices: return QStringLiteral("choices");
    case Phase::Submitting: return QStringLiteral("submitting");
    case Phase::Armed: return QStringLiteral("armed");
    case Phase::Paused: return QStringLiteral("paused");
    case Phase::Reconciling: return QStringLiteral("reconciling");
    }
    return {};
}

QString reasonText(Reason reason, Provider provider) {
    const bool claude = provider == Provider::Claude;
    switch (reason) {
    case Reason::None: return {};
    case Reason::Unavailable: return QStringLiteral("Usage is unavailable right now.");
    case Reason::Stale: return QStringLiteral("Refresh usage first.");
    case Reason::NoCredits: return QStringLiteral("No banked reset is available.");
    case Reason::Unbound: return QStringLiteral("The account could not be identified.");
    case Reason::BelowFloor: return claude ? QStringLiteral("Session and weekly usage are below 90%.")
        : QStringLiteral("Weekly usage is below 90%.");
    case Reason::ProviderRefused: return QStringLiteral("Anthropic does not allow a reset right now.");
    case Reason::ProviderUnknown: return QStringLiteral("Anthropic has not reported whether a reset can be used.");
    case Reason::AwaitingUsage: return QStringLiteral("Waiting for updated usage after the previous request.");
    case Reason::Busy: return QStringLiteral("A reset request is in progress.");
    case Reason::ForeignOwner: return QStringLiteral("Finish the %1 reset first.")
        .arg(claude ? QStringLiteral("ChatGPT") : QStringLiteral("Claude"));
    case Reason::WindowUnknown: return QStringLiteral("The weekly reset time is unknown.");
    case Reason::WindowEnded: return QStringLiteral("The weekly window has ended.");
    case Reason::AlreadyArmed: return QStringLiteral("Automatic use is already set.");
    case Reason::ConfirmationStale: return QStringLiteral("The account or connection changed. Close and reopen.");
    case Reason::IdentityChanged: return QStringLiteral("The account, connection, or weekly window changed.");
    }
    return {};
}

QString cancellationText(Reason reason) {
    switch (reason) {
    case Reason::IdentityChanged: return QStringLiteral("Automatic reset canceled because the account, connection, or weekly window changed.");
    case Reason::BelowFloor: return QStringLiteral("Automatic reset canceled because weekly usage is below 90%.");
    case Reason::NoCredits: return QStringLiteral("Automatic reset canceled because no banked resets are available.");
    case Reason::AwaitingUsage: return QStringLiteral("Automatic reset canceled because a reset has already been requested.");
    case Reason::WindowEnded: case Reason::WindowUnknown:
        return QStringLiteral("Automatic reset canceled because the weekly window ended.");
    default: return QStringLiteral("Automatic reset canceled. No reset was requested.");
    }
}

QString awaitingUsageText(Provider provider, bool uncertain) {
    if (provider == Provider::Claude) return uncertain
        ? QStringLiteral("The reset result is unknown. Waiting for updated Claude usage before allowing another reset.")
        : QStringLiteral("A reset has already been requested. Waiting for updated Claude usage.");
    return uncertain
        ? QStringLiteral("The reset result is unknown. Waiting for weekly usage to drop below 90% before allowing another reset.")
        : QStringLiteral("A reset has already been requested. Waiting for weekly usage to drop below 90%.");
}
}
