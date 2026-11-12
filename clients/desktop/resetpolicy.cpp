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

bool known(std::optional<double> value) { return value && std::isfinite(*value) && *value >= 0; }

QString joinHuman(const QStringList &items, const QString &conjunction) {
    if (items.isEmpty()) return {};
    if (items.size() == 1) return items.first();
    return items.mid(0, items.size() - 1).join(QStringLiteral(", ")) + conjunction + items.last();
}

QString bucketLabel(const ResetPolicy::Facts &facts, const QString &id) {
    for (const auto &bucket : facts.buckets) if (bucket.id == id) return bucket.label;
    return {};
}

// Anthropic's own wording for the next grant when the server supplied one,
// otherwise our name for the kind. The quotes are curly to match user-facing
// copy without putting non-ASCII in this source file.
QString grantName(const ResetPolicy::Kind &kind) {
    if (!kind.nextLabel.isEmpty())
        return QString(QChar(0x201C)) + kind.nextLabel + QChar(0x201D);
    return QStringLiteral("one ") + kind.label;
}

}

namespace ResetPolicy {

Capabilities evaluate(const Facts &facts) {
    const bool ownedBusy = facts.owner && facts.busy;
    const bool ownedConfirmed = facts.owner && facts.confirmed;
    // A server-held automatic reset belongs to its provider, not to the dialog
    // slot, so it neither hides nor blocks the other provider's options.
    const bool foreign = !facts.owner && (facts.busy || facts.confirmed);
    const bool credits = facts.availableCount && *facts.availableCount > 0;
    const bool atOfferFloor = known(facts.utilization) && *facts.utilization >= offerFloor;
    const bool atCodexFloor = known(facts.utilization) && *facts.utilization >= codexManualFloor;
    const bool kindAware = facts.provider == Provider::Claude && facts.kindsKnown && !facts.kinds.isEmpty();
    // A choice that left the reading is its own state: it never becomes "any".
    const bool selectionMissing = kindAware && facts.selectedKindMissing;
    const Kind *selected = kindAware ? findKind(facts, facts.selectedKind) : nullptr;
    const bool anyKindUsable = kindAware && std::any_of(facts.kinds.cbegin(), facts.kinds.cend(),
        [](const Kind &kind) { return kind.availableCount > 0 && kind.usableNow; });
    const bool claudeUsable = facts.provider == Provider::Claude && facts.usableNow && *facts.usableNow;
    const bool offered = credits && facts.accountBound && (atOfferFloor || claudeUsable || anyKindUsable);
    Capabilities result;
    // Owned context stays visible even when the current reading is unavailable.
    result.visible = ownedBusy || facts.automationArmed || ownedConfirmed || facts.awaitingUsage || offered;
    if (!result.visible) return result;
    // An arm pauses while the provider or backend reports no reading; a
    // reading that is merely older than the freshness window keeps monitoring.
    result.phase = ownedBusy ? Phase::Submitting
        : facts.automationArmed ? (facts.readingAvailable ? Phase::Armed : Phase::Paused)
        : facts.awaitingUsage ? Phase::Reconciling : Phase::Choices;
    result.openReason = foreign ? Reason::ForeignOwner : facts.busy ? Reason::Busy : Reason::None;
    result.canOpen = result.openReason == Reason::None;
    result.canCancelArm = facts.automationArmed && !facts.automationPending;
    // Blockers shared by both choices, most specific first. Visibility is
    // never permission: a stale or latched reading opens for information only.
    const Reason shared = foreign ? Reason::ForeignOwner : facts.busy ? Reason::Busy
        : facts.automationPending ? Reason::Busy
        : facts.awaitingUsage ? Reason::AwaitingUsage : !facts.readingAvailable ? Reason::Unavailable
        : !facts.fresh ? Reason::Stale : !credits ? Reason::NoCredits
        : !facts.accountBound ? Reason::Unbound : Reason::None;
    if (shared != Reason::None) result.useNowReason = shared;
    else if (selectionMissing) result.useNowReason = Reason::KindMissing;
    else if (kindAware) {
        // A kind's own answer is authoritative once the reading lists kinds.
        // "Any matching reset" cannot spend now because Use now must name one.
        if (!selected) result.useNowReason = Reason::KindRequired;
        else if (selected->availableCount <= 0) result.useNowReason = Reason::NoCredits;
        else if (!selected->usableNow) result.useNowReason = Reason::ProviderRefused;
        else result.useNowReason = Reason::None;
    }
    else if (facts.provider == Provider::Codex) result.useNowReason = atCodexFloor ? Reason::None : Reason::BelowFloor;
    else result.useNowReason = claudeUsable ? Reason::None
        : facts.usableNow ? Reason::ProviderRefused : Reason::ProviderUnknown;
    // Arming is independent of immediate permission: Claude may arm without
    // usable_now, which the server's firing reading must still report. The
    // server revalidates every rule; this only decides what to offer.
    if (facts.automationArmed) result.armReason = Reason::AlreadyArmed;
    else if (!facts.automationSupported) result.armReason = Reason::ServerUnsupported;
    else if (shared != Reason::None) result.armReason = shared;
    else if (selectionMissing) result.armReason = Reason::KindMissing;
    else if (!offered) result.armReason = Reason::BelowFloor;
    else if (facts.provider == Provider::Claude) {
        // The server needs at least one kind to arm Claude, so a reading with
        // no kinds (absent, null, or empty) offers nothing to authorize. Codex
        // sends no kinds and keeps its count-only request.
        const QStringList chosen = automaticKinds(facts);
        if (chosen.isEmpty()) result.armReason = Reason::NoCredits;
        else {
            const bool applies = std::any_of(chosen.cbegin(), chosen.cend(), [&](const QString &id) {
                const Kind *kind = findKind(facts, id);
                return kind && kindApplies(facts, *kind);
            });
            if (!applies) result.armReason = Reason::NoTrigger;
            else if (facts.window == Window::Future) result.armReason = Reason::None;
            else if (facts.window == Window::Ended) result.armReason = Reason::WindowEnded;
            else result.armReason = Reason::WindowUnknown;
        }
    }
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

bool releasesCodexReceipt(std::optional<double> utilization) {
    return known(utilization) && *utilization < codexManualFloor;
}

const Kind *findKind(const Facts &facts, const std::optional<QString> &id) {
    if (!id) return nullptr;
    return findKind(facts, *id);
}

const Kind *findKind(const Facts &facts, const QString &id) {
    if (id.isEmpty()) return nullptr;
    for (const auto &kind : facts.kinds) if (kind.id == id) return &kind;
    return nullptr;
}

bool kindApplies(const Facts &facts, const Kind &kind) {
    return std::any_of(facts.buckets.cbegin(), facts.buckets.cend(), [&](const Bucket &bucket) {
        return kind.refills.contains(bucket.id)
            || (bucket.id == QStringLiteral("weekly") && kind.givesBackWeekly);
    });
}

QStringList automaticKinds(const Facts &facts) {
    const bool kindAware = facts.provider == Provider::Claude && facts.kindsKnown && !facts.kinds.isEmpty();
    if (!kindAware || facts.selectedKindMissing) return {};
    if (const Kind *selected = findKind(facts, facts.selectedKind))
        return selected->availableCount > 0 ? QStringList{selected->id} : QStringList{};
    QStringList chosen;
    for (const auto &kind : facts.kinds) if (kind.availableCount > 0) chosen.append(kind.id);
    return chosen;
}

QString kindCountText(const Kind &kind) {
    return QStringLiteral("%1 %2%3").arg(kind.availableCount).arg(kind.label,
        kind.availableCount == 1 ? QString() : QStringLiteral("s"));
}

QString kindTitle(const Kind &kind) {
    // Capitalize the first letter only; provider labels keep their own casing.
    if (kind.label.isEmpty()) return kind.label;
    QString result = kind.label;
    result[0] = result[0].toUpper();
    return result;
}

QString kindRefillText(const Facts &facts, const Kind &kind) {
    QStringList labels;
    for (const auto &refill : kind.refills) {
        const QString label = bucketLabel(facts, refill);
        if (!label.isEmpty() && !labels.contains(label)) labels.append(label);
    }
    return labels.isEmpty() ? QString() : QStringLiteral("Refills ") + joinHuman(labels, QStringLiteral(" and "));
}

QString kindApplicabilityText(const Facts &facts, const Kind &kind) {
    QStringList labels;
    for (const auto &bucket : facts.buckets) {
        const bool applies = kind.refills.contains(bucket.id)
            || (bucket.id == QStringLiteral("weekly") && kind.givesBackWeekly);
        if (!applies) continue;
        const QString label = bucket.label.isEmpty() ? bucket.id : bucket.label;
        if (!labels.contains(label)) labels.append(label);
    }
    return labels.isEmpty() ? QString()
        : QStringLiteral("Used when %1 reaches 100%").arg(joinHuman(labels, QStringLiteral(" or ")));
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

QString kindDetailText(const Facts &facts, const Kind &kind, const QString &expiryLabel) {
    QStringList parts{kindCountText(kind)};
    const QString refills = kindRefillText(facts, kind);
    if (!refills.isEmpty()) parts.append(refills);
    const QString returned = kindReturnText(facts, kind);
    if (!returned.isEmpty()) parts.append(returned);
    if (!expiryLabel.isEmpty()) parts.append(QStringLiteral("Expires ") + expiryLabel);
    return parts.join(QStringLiteral(" %1 ").arg(QChar(0x00B7)));
}

QString kindNextText(const Kind &kind, const QString &expiryLabel) {
    if (!kind.hasNext) return {};
    QString text = QStringLiteral("Next: ") + grantName(kind);
    if (!expiryLabel.isEmpty()) text += QStringLiteral(", expires ") + expiryLabel;
    return text;
}

QString kindUseNowText(const Kind &kind, const QString &expiryLabel) {
    if (!kind.hasNext) return {};
    QString text = QStringLiteral("Uses ") + grantName(kind);
    if (!expiryLabel.isEmpty()) text += QStringLiteral(", which expires ") + expiryLabel;
    return text + QStringLiteral(".");
}

QString automaticEffectText(const Facts &facts) {
    const bool kindAware = facts.provider == Provider::Claude && facts.kindsKnown && !facts.kinds.isEmpty();
    if (!kindAware || facts.selectedKindMissing) return {};
    if (const Kind *selected = findKind(facts, facts.selectedKind))
        return kindApplicabilityText(facts, *selected);
    return facts.kinds.size() > 1
        ? QStringLiteral("The matching reset is used at whichever limit is reached.") : QString();
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
    case Reason::KindRequired: return QStringLiteral("Choose which banked reset to use now.");
    case Reason::KindMissing: return QStringLiteral("The banked reset you chose is no longer available. Choose a banked reset again.");
    case Reason::NoTrigger: return QStringLiteral("This reset does not refill a usage meter on this account.");
    case Reason::ServerUnsupported: return QStringLiteral("Update the server to use automatic resets.");
    }
    return {};
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
