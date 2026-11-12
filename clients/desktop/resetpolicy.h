#pragma once
// Pure banked-reset capability policy: typed facts in, capabilities out.
// It decides what a card may show, which option a user may choose, and how an
// armed one-shot authorization reacts to a snapshot. It never reads usage,
// touches receipts, or performs an action: the Controller gathers the facts
// and owns every request. DO NOT turn these decisions into test actions; a
// Submit outcome is an eligibility fact, never something a test may execute.
#include <QString>
#include <QStringList>
#include <QVector>
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
    IdentityChanged, KindRequired, KindUnavailable, NoTrigger
};
enum class ArmOutcome { Keep, Wait, Cancel, Submit };

// One additive kind of banked reset, as the server reports it.
struct Kind {
    QString id;
    QString label;
    qint64 availableCount = 0;
    bool usableNow = false;
    QStringList refills;
    bool givesBackWeekly = false;
    QString expiresAt; // empty when the server reported no expiry
    // The specific grant a redemption would spend right now, when the server
    // reports one. nextLabel is Anthropic's own wording and may be empty;
    // nextExpiresAt is empty when that grant has no expiry.
    bool hasNext = false;
    QString nextLabel;
    QString nextExpiresAt;
};
// One meter in the provider reading, used for labels and trigger ownership.
struct Bucket {
    QString id;
    QString label;
    std::optional<double> utilization;
};

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
    // The server's additive kind breakdown. kindsKnown means the reading
    // explicitly reported an array; that array may itself be empty.
    bool kindsKnown = false;
    QVector<Kind> kinds;
    QVector<Bucket> buckets;            // the provider's meters, for labels and triggers
    std::optional<QString> selectedKind; // the dialog's choice, if any
    std::optional<QString> armKind;      // the armed choice; empty for a legacy arm
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
// The kind whose id matches, or nullptr. Unknown ids never match.
const Kind *findKind(const Facts &facts, const std::optional<QString> &id);
const Kind *findKind(const Facts &facts, const QString &id);
// Meters a kind may fire on: its refills that are present in the reading,
// minus every bucket owned by a listed kind with a positive count and
// strictly fewer refills (the narrower kind owns its bucket).
QStringList triggerBuckets(const Facts &facts, const QString &kindId);
// Highest present trigger utilization, or nothing when none can be read.
std::optional<double> triggerUtilization(const Facts &facts, const QString &kindId);
QString kindCountText(const Kind &kind);
QString kindTitle(const Kind &kind);
QString kindRefillText(const Facts &facts, const Kind &kind);
QString kindTriggerText(const Facts &facts, const Kind &kind);
QString kindReturnText(const Facts &facts, const Kind &kind);
// The single implementation of kind wording for both the card and the
// options dialog. expiryLabel is the already-localized reset time for the
// relevant timestamp; an empty label omits the expiry clause.
QString kindDetailText(const Facts &facts, const Kind &kind, const QString &expiryLabel);
QString kindNextText(const Kind &kind, const QString &expiryLabel);
QString kindUseNowText(const Kind &kind, const QString &expiryLabel);

QString phaseName(Phase phase);
QString reasonText(Reason reason, Provider provider);
QString cancellationText(Reason reason);
QString awaitingUsageText(Provider provider, bool uncertain);
}
