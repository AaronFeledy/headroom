#pragma once
// Pure banked-reset capability policy: typed facts in, capabilities out.
// It decides what a card may show and which option a user may choose. It never
// reads usage, touches receipts, or performs an action: the Controller gathers
// the facts and owns every request. Automatic resets are evaluated by the
// server, so this policy can only offer an arm and cancel one; it has no
// outcome that spends. DO NOT turn these decisions into test actions.
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
    KindRequired, KindMissing, NoTrigger, ServerUnsupported
};

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
// One meter in the provider reading, used for labels and applicability.
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
    // The server-owned automatic reset. Supported only when the row carried a
    // well-formed reset_automation object; the desktop never keeps its own arm.
    bool automationSupported = false;
    bool automationArmed = false;
    QStringList automationKinds;       // the allowed kinds the server stored
    QString automationWindowEndsAt;
    // Session state. One provider owns the confirmation or submission.
    bool awaitingUsage = false;        // a durable receipt latch is pending
    bool busy = false;                 // a submission is in flight
    // An arm or cancel request is in flight. It blocks every choice while the
    // dialog keeps its phase; it is not the manual-spend busy state.
    bool automationPending = false;
    bool owner = false;                // this provider owns the single dialog slot
    bool confirmed = false;            // options open, bound to an identity; not consent to spend
    bool confirmationCurrent = false;  // that binding matches the current identity
    // The server's additive kind breakdown. kindsKnown means the reading
    // explicitly reported an array; that array may itself be empty.
    bool kindsKnown = false;
    QVector<Kind> kinds;
    QVector<Bucket> buckets;            // the provider's meters, for labels and applicability
    std::optional<QString> selectedKind; // the dialog's choice, if any; empty means any matching
    // True when that choice is absent from the current reading. It is its own
    // state: a specific choice is never widened to any matching kind.
    bool selectedKindMissing = false;
};

struct Capabilities {
    Phase phase = Phase::Hidden;
    bool visible = false;      // the card offers the entry control
    bool canOpen = false;      // the entry control opens the options
    bool canUseNow = false;    // immediate use is permitted for this reading
    bool canConfirm = false;   // canUseNow within current-identity options; Use now is the consent
    bool canArm = false;       // the server may store an automatic authorization
    bool canSchedule = false;  // canArm within current-identity options; the automatic choice is the consent
    bool canCancelArm = false; // a server-held arm may be canceled, fresh or not
    Reason openReason = Reason::None;
    Reason useNowReason = Reason::None;
    Reason armReason = Reason::None;
};

Capabilities evaluate(const Facts &facts);
// A successful reading below the manual floor releases a Codex receipt latch.
bool releasesCodexReceipt(std::optional<double> utilization);
// The kind whose id matches, or nullptr. Unknown ids never match.
const Kind *findKind(const Facts &facts, const std::optional<QString> &id);
const Kind *findKind(const Facts &facts, const QString &id);
// A kind applies to a present meter when the meter is named by its refills, or
// when the meter is weekly and the kind gives usage back to the weekly limit.
bool kindApplies(const Facts &facts, const Kind &kind);
// The kinds one automatic choice would authorize: the selected kind, otherwise
// every listed kind with a positive count. Empty when none can be armed.
QStringList automaticKinds(const Facts &facts);
QString kindCountText(const Kind &kind);
QString kindTitle(const Kind &kind);
QString kindRefillText(const Facts &facts, const Kind &kind);
QString kindApplicabilityText(const Facts &facts, const Kind &kind);
QString kindReturnText(const Facts &facts, const Kind &kind);
// The single implementation of kind wording for both the card and the
// options dialog. expiryLabel is the already-localized reset time for the
// relevant timestamp; an empty label omits the expiry clause.
QString kindDetailText(const Facts &facts, const Kind &kind, const QString &expiryLabel);
QString kindNextText(const Kind &kind, const QString &expiryLabel);
QString kindUseNowText(const Kind &kind, const QString &expiryLabel);
// What the automatic choice authorizes, in words. Empty when kinds do not
// apply (Codex and legacy Claude keep their dialog copy).
QString automaticEffectText(const Facts &facts);

QString phaseName(Phase phase);
QString reasonText(Reason reason, Provider provider);
QString awaitingUsageText(Provider provider, bool uncertain);
}
