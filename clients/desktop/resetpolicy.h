#pragma once
// Pure banked-reset capability policy: typed facts in, capabilities out.
// It decides what a card may show, which option a user may choose, and how an
// armed one-shot authorization reacts to a snapshot. It never reads usage,
// touches receipts, or performs an action: the Controller gathers the facts
// and owns every request. DO NOT turn these decisions into test actions; a
// Submit outcome is an eligibility fact, never something a test may execute.
#include <QString>
#include <QtGlobal>
#include <optional>

namespace ResetPolicy {
enum class Provider { Codex, Claude };
// A weekly reset deadline relative to now.
enum class Window { Unknown, Future, Ended };
enum class Phase { Hidden, Choices, Submitting, Armed, Paused, Reconciling };
enum class Reason {
    None, Unavailable, Stale, NoCredits, Unbound, BelowFloor, ProviderRefused, ProviderUnknown,
    AwaitingUsage, Busy, ForeignOwner, WindowUnknown, WindowEnded, AlreadyArmed, ConfirmationStale,
    IdentityChanged
};
enum class ArmOutcome { Keep, Wait, Cancel, Submit };

struct Facts {
    Provider provider = Provider::Codex;
    // The current reading. Optional values are unknown when the server omitted
    // or invalidated them; an unknown value never satisfies a requirement.
    bool readingAvailable = false;   // the provider succeeded in the current snapshot
    bool fresh = false;              // ready backend, recent snapshot, provider fetch anchor
    std::optional<qint64> availableCount;
    std::optional<double> utilization; // Codex: weekly; Claude: max(session, weekly)
    std::optional<bool> usableNow;     // Claude's explicit flag; unknown when null
    bool accountBound = false;         // valid opaque account fingerprint
    Window window = Window::Unknown;   // the reading's weekly deadline
    // Session state. One provider owns the confirmation, arm, or submission.
    bool awaitingUsage = false;        // a durable receipt latch is pending
    bool busy = false;                 // a submission is in flight
    bool owner = false;                // this provider owns the single slot
    bool confirmed = false;            // options open, bound to an identity; not consent to spend
    bool confirmationCurrent = false;  // that binding matches the current identity
    bool armed = false;                // the owner has a one-shot automatic authorization
    bool armIdentityCurrent = false;   // the arm matches the current identity
    bool armWindowCurrent = false;     // the reading's weekly window is the arm's window
    Window armWindow = Window::Unknown; // the arm's recorded deadline; it ends even offline
};

struct Capabilities {
    Phase phase = Phase::Hidden;
    bool visible = false;      // the card offers the entry control
    bool canOpen = false;      // the entry control opens the options
    bool canUseNow = false;    // immediate use is permitted for this reading
    bool canConfirm = false;   // canUseNow within current-identity options; Use now is the consent
    bool canArm = false;       // a one-shot automatic authorization is permitted
    bool canSchedule = false;  // canArm within current-identity options; the automatic choice is the consent
    bool canCancelArm = false; // an owned arm may be canceled, fresh or not
    Reason openReason = Reason::None;
    Reason useNowReason = Reason::None;
    Reason armReason = Reason::None;
};

struct ArmDecision {
    ArmOutcome outcome = ArmOutcome::Keep;
    Reason reason = Reason::None; // set for Cancel
};

Capabilities evaluate(const Facts &facts);
// How an owned arm reacts to the current facts. Submit only reports that
// usage is at the limit and immediate use is permitted right now.
ArmDecision observeArm(const Facts &facts);
// WindowEnded or WindowUnknown once an arm's recorded deadline no longer lies ahead.
Reason armExpiry(Window armWindow);
// A successful reading below the manual floor releases a Codex receipt latch.
bool releasesCodexReceipt(std::optional<double> utilization);

QString phaseName(Phase phase);
QString reasonText(Reason reason, Provider provider);
QString cancellationText(Reason reason);
QString awaitingUsageText(Provider provider, bool uncertain);
}
