#include "resetpolicy.h"
#include <algorithm>
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

// Capitalize the first letter only; provider labels keep their own casing.
QString capitalizeFirst(const QString &text) {
    if (text.isEmpty()) return text;
    QString result = text;
    result[0] = result[0].toUpper();
    return result;
}

QString joinHuman(const QStringList &items, const QString &conjunction) {
    if (items.isEmpty()) return {};
    if (items.size() == 1) return items.first();
    return items.mid(0, items.size() - 1).join(QStringLiteral(", ")) + conjunction + items.last();
}

QString bucketLabel(const ResetPolicy::Facts &facts, const QString &id) {
    for (const auto &bucket : facts.buckets) if (bucket.id == id) return bucket.label;
    return {};
}
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
    const bool kindAware = facts.provider == Provider::Claude && facts.kindsKnown && !facts.kinds.isEmpty();
    const Kind *selected = kindAware ? findKind(facts, facts.selectedKind) : nullptr;
    const bool anyKindUsable = kindAware && std::any_of(facts.kinds.cbegin(), facts.kinds.cend(),
        [](const Kind &kind) { return kind.availableCount > 0 && kind.usableNow; });
    const bool claudeUsable = facts.provider == Provider::Claude && facts.usableNow && *facts.usableNow;
    const bool offered = credits && facts.accountBound && (atOfferFloor || claudeUsable || anyKindUsable);
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
    else if (kindAware) {
        // A kind's own answer is authoritative once the reading lists kinds.
        if (!selected) result.useNowReason = Reason::KindRequired;
        else if (selected->availableCount <= 0) result.useNowReason = Reason::NoCredits;
        else if (!selected->usableNow) result.useNowReason = Reason::ProviderRefused;
        else result.useNowReason = Reason::None;
    }
    else if (facts.provider == Provider::Codex) result.useNowReason = atCodexFloor ? Reason::None : Reason::BelowFloor;
    else result.useNowReason = claudeUsable ? Reason::None
        : facts.usableNow ? Reason::ProviderRefused : Reason::ProviderUnknown;
    // Arming is independent of immediate permission: Claude may arm without
    // usable_now, which the firing reading must still report. A kind arm also
    // needs a present trigger meter, and the user must choose among several.
    if (ownedArmed) result.armReason = Reason::AlreadyArmed;
    else if (shared != Reason::None) result.armReason = shared;
    else if (!offered) result.armReason = Reason::BelowFloor;
    else if (kindAware && !selected) result.armReason = Reason::KindRequired;
    else if (kindAware && selected->availableCount <= 0) result.armReason = Reason::NoCredits;
    else if (kindAware && triggerBuckets(facts, selected->id).isEmpty()) result.armReason = Reason::NoTrigger;
    else if (facts.window == Window::Future) result.armReason = Reason::None;
    else if (facts.window == Window::Ended) result.armReason = Reason::WindowEnded;
    else result.armReason = Reason::WindowUnknown;
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
    // Once the server reports kinds, the arm belongs to exactly one of them.
    // A legacy arm cannot choose one safely, and a vanished or exhausted kind
    // retires through the same path as other lost authorizations. A kind-bound
    // arm also retires when the server stops reporting kinds at all: without
    // the breakdown the chosen kind can no longer be verified before spending.
    if (facts.provider == Provider::Claude && !facts.kindsKnown && facts.armKind)
        return {ArmOutcome::Cancel, Reason::KindUnavailable};
    if (facts.provider == Provider::Claude && facts.kindsKnown) {
        const Kind *kind = findKind(facts, facts.armKind);
        if (!kind || kind->availableCount <= 0) return {ArmOutcome::Cancel, Reason::KindUnavailable};
        if (triggerBuckets(facts, kind->id).isEmpty()) return {ArmOutcome::Cancel, Reason::NoTrigger};
        const auto trigger = triggerUtilization(facts, kind->id);
        if (!trigger) return {ArmOutcome::Wait, Reason::None};
        if (facts.awaitingUsage) return {ArmOutcome::Cancel, Reason::AwaitingUsage};
        if (!kind->usableNow) return {ArmOutcome::Wait, Reason::None};
        // A stale reading at the limit keeps the arm; it cannot prove a spend.
        if (!facts.fresh) return {};
        if (*trigger < usageLimit) return {};
        // Firing needs every blocker Use now has, judged for the armed kind
        // and not for whatever the dialog currently has selected.
        Facts bound = facts;
        bound.selectedKind = facts.armKind;
        if (evaluate(bound).canUseNow) return {ArmOutcome::Submit, Reason::None};
        return {};
    }
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

const Kind *findKind(const Facts &facts, const std::optional<QString> &id) {
    if (!id || id->isEmpty()) return nullptr;
    return findKind(facts, *id);
}

const Kind *findKind(const Facts &facts, const QString &id) {
    if (id.isEmpty()) return nullptr;
    for (const auto &kind : facts.kinds) if (kind.id == id) return &kind;
    return nullptr;
}

QStringList triggerBuckets(const Facts &facts, const QString &kindId) {
    const Kind *kind = findKind(facts, kindId);
    if (!kind) return {};
    QStringList present;
    for (const auto &refill : kind->refills) {
        const bool inReading = std::any_of(facts.buckets.cbegin(), facts.buckets.cend(),
            [&](const Bucket &bucket) { return bucket.id == refill; });
        if (inReading) present.append(refill);
    }
    for (const auto &other : facts.kinds) {
        if (other.id == kindId || other.availableCount <= 0 || other.refills.size() >= kind->refills.size())
            continue;
        for (const auto &refill : other.refills) present.removeAll(refill);
    }
    return present;
}

std::optional<double> triggerUtilization(const Facts &facts, const QString &kindId) {
    bool found = false;
    double highest = 0;
    for (const auto &id : triggerBuckets(facts, kindId)) {
        for (const auto &bucket : facts.buckets) {
            if (bucket.id != id || !known(bucket.utilization)) continue;
            highest = found ? qMax(highest, *bucket.utilization) : *bucket.utilization;
            found = true;
        }
    }
    return found ? std::optional<double>(highest) : std::nullopt;
}

QString kindCountText(const Kind &kind) {
    return QStringLiteral("%1 %2%3").arg(kind.availableCount).arg(kind.label,
        kind.availableCount == 1 ? QString() : QStringLiteral("s"));
}

QString kindTitle(const Kind &kind) {
    return capitalizeFirst(kind.label);
}

QString kindRefillText(const Facts &facts, const Kind &kind) {
    QStringList labels;
    for (const auto &refill : kind.refills) {
        const QString label = bucketLabel(facts, refill);
        if (!label.isEmpty() && !labels.contains(label)) labels.append(label);
    }
    return labels.isEmpty() ? QString() : QStringLiteral("Refills ") + joinHuman(labels, QStringLiteral(" and "));
}

QString kindTriggerText(const Facts &facts, const Kind &kind) {
    QStringList labels;
    for (const auto &id : triggerBuckets(facts, kind.id)) {
        QString label = bucketLabel(facts, id);
        if (label.isEmpty()) label = id;
        if (!labels.contains(label)) labels.append(label);
    }
    return labels.isEmpty() ? QString()
        : QStringLiteral("Uses it when %1 reaches 100%").arg(joinHuman(labels, QStringLiteral(" or ")));
}

QString kindReturnText(const Facts &facts, const Kind &kind) {
    if (!kind.givesBackWeekly) return {};
    for (const auto &refill : kind.refills) {
        if (refill == QStringLiteral("weekly")) continue;
        const QString label = bucketLabel(facts, refill);
        if (!label.isEmpty())
            return QStringLiteral("Returns what %1 used to your weekly limit").arg(label);
    }
    return {};
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
    case Reason::KindRequired: return QStringLiteral("Choose which banked reset to use.");
    case Reason::KindUnavailable: return QStringLiteral("That banked reset is no longer available.");
    case Reason::NoTrigger: return QStringLiteral("This reset does not refill a usage meter on this account.");
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
    case Reason::KindUnavailable:
        return QStringLiteral("Automatic reset canceled because that banked reset is no longer available.");
    case Reason::NoTrigger:
        return QStringLiteral("Automatic reset canceled because that banked reset no longer refills a usage meter.");
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
