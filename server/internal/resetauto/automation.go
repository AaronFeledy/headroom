package resetauto

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	// ReasonUser reports an explicit cancellation by the owner.
	ReasonUser = "user"
	// ReasonManualReset reports that an explicit manual reset replaced the
	// automatic authorization.
	ReasonManualReset = "manual_reset"

	// Failure reason codes reported when a fired spend does not complete cleanly.
	ReasonOutcomeUnknown   = "outcome_unknown"
	ReasonRefused          = "refused"
	ReasonStateUnavailable = "state_unavailable"

	resetBudget     = 60 * time.Second
	stateWriteBound = 5 * time.Second
)

// ExecutorRequest is one validated spend request for the shared reset core.
type ExecutorRequest struct {
	Provider           string
	RequestID          string
	AccountFingerprint string
	Kind               string
}

// ExecutorResult mirrors the reset core's terminal classification without
// exposing provider details.
type ExecutorResult struct {
	Outcome   string
	Ambiguous bool
	// Unavailable is set when the reset core could not attempt a submission.
	Unavailable bool
}

// Executor performs one spend. It is injected so tests can never construct a
// component that reaches a provider consume call.
type Executor func(context.Context, ExecutorRequest) ExecutorResult

type Options struct {
	// Dir is the stable reset-automation directory; blank disables automation.
	Dir string
	// Executor is the shared reset core. Nil keeps every fire decision.
	Executor Executor
	// Lifetime bounds evaluation and firing to the server process.
	Lifetime context.Context
	Now      func() time.Time
	NewID    func() (string, error)
	Logger   *slog.Logger
}

// Automation is the single process-wide coordinator. Handlers share one
// instance, so there is one evaluation path and one firing path.
type Automation struct {
	store    *Store
	executor Executor
	lifetime context.Context
	now      func() time.Time
	newID    func() (string, error)
	logger   *slog.Logger
}

func New(opts Options) *Automation {
	lifetime := opts.Lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = newRequestID
	}
	return &Automation{store: NewStore(opts.Dir), executor: opts.Executor, lifetime: lifetime, now: now, newID: newID, logger: opts.Logger}
}

// Observe runs after a completed poll, with the provider's fetch lock already
// released. It re-reads disk state and never trusts an earlier evaluation.
func (a *Automation) Observe(name string, entry poller.Entry) {
	if a == nil {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(name))
	if provider != ProviderClaude && provider != ProviderCodex {
		return
	}
	records, err := a.store.Load()
	if err != nil {
		return
	}
	record := records[provider]
	if !record.Armed() {
		return
	}
	if a.lifetime.Err() != nil {
		return
	}
	decision := Decide(provider, record, entry, a.now().UTC())
	switch decision.Action {
	case ActionCancel:
		ctx, cancel := context.WithTimeout(a.lifetime, stateWriteBound)
		defer cancel()
		if _, err := a.store.CancelIfEvaluation(ctx, provider, record, decision.Reason); err != nil {
			a.log("automatic reset cancel failed", provider, err)
		}
	case ActionFire:
		a.fire(provider, record, decision.Kind)
	}
}

func (a *Automation) fire(provider string, record Record, kind string) {
	if a.executor == nil {
		// No firing function is configured, so every fire decision is a keep.
		return
	}
	ctx, cancel := context.WithTimeout(a.lifetime, resetBudget)
	defer cancel()
	if err := a.store.BeginFire(ctx, provider, record, kind); err != nil {
		if !errors.Is(err, ErrStaleAuthorization) {
			a.log("automatic reset could not begin", provider, err)
		}
		return
	}
	requestID, err := a.newID()
	if err != nil {
		a.finish(provider, newEvent(a.now(), "failed", "", ReasonStateUnavailable, kind))
		return
	}
	result := a.executor(ctx, ExecutorRequest{Provider: provider, RequestID: requestID, AccountFingerprint: record.AccountFingerprint, Kind: kind})
	a.finish(provider, classify(result, kind, a.now()))
}

func (a *Automation) finish(provider string, event *usage.ResetAutomationEvent) {
	// The spend may consume the whole firing budget; the local receipt write
	// still needs its own bound so a completed reset is always recorded.
	ctx, cancel := context.WithTimeout(a.lifetime, stateWriteBound)
	defer cancel()
	if err := a.store.FinishFire(ctx, provider, event); err != nil {
		a.log("automatic reset result was not saved", provider, err)
	}
}

// Arm validates and stores one authorization for the cached reading.
func (a *Automation) Arm(ctx context.Context, provider string, request ArmRequest, entry poller.Entry) (*usage.ResetAutomationState, error) {
	if a == nil || !a.store.Available() {
		return nil, ErrStateUnavailable
	}
	if code := ValidateArm(provider, entry, request, a.now().UTC()); code != "" {
		return nil, NotEligibleError{Code: code}
	}
	record, err := a.store.Arm(ctx, provider, request)
	if err != nil {
		return nil, err
	}
	return Project(record, a.now().UTC()), nil
}

// Cancel removes any stored authorization and records user cancellation.
func (a *Automation) Cancel(ctx context.Context, provider string) (*usage.ResetAutomationState, error) {
	if a == nil || !a.store.Available() {
		return nil, ErrStateUnavailable
	}
	record, err := a.store.Cancel(ctx, provider, ReasonUser)
	if err != nil {
		return nil, err
	}
	return Project(record, a.now().UTC()), nil
}

// CancelManualReset applies the preference that pressing the manual reset
// button replaces an armed automatic authorization. Failure is logged and the
// manual reset continues: this is a preference, not a spend guard.
func (a *Automation) CancelManualReset(ctx context.Context, provider string) {
	if a == nil {
		return
	}
	if !a.store.Available() {
		a.log("automatic reset cancel unavailable", provider, ErrStateUnavailable)
		return
	}
	if _, err := a.store.Cancel(ctx, provider, ReasonManualReset); err != nil {
		a.log("automatic reset cancel failed", provider, err)
	}
}

// State reports the stored authorization. The bool is false when automation is
// unavailable on this server, which callers report as a null key.
func (a *Automation) State(provider string) (*usage.ResetAutomationState, bool) {
	if a == nil || !a.store.Available() {
		return nil, false
	}
	records, err := a.store.Load()
	if err != nil {
		return &usage.ResetAutomationState{}, true
	}
	return Project(records[provider], a.now().UTC()), true
}

// Project converts a stored record into the API state. A window that has ended
// is never reported as armed.
func Project(record Record, now time.Time) *usage.ResetAutomationState {
	state := &usage.ResetAutomationState{LastEvent: record.LastEvent}
	if !record.Armed() || !now.Before(record.WindowEndsAt) {
		return state
	}
	armedAt := formatStateTime(record.ArmedAt)
	windowEndsAt := formatStateTime(record.WindowEndsAt)
	fingerprint := record.AccountFingerprint
	state.Armed = true
	state.ArmedAt = &armedAt
	state.WindowEndsAt = &windowEndsAt
	state.AccountFingerprint = &fingerprint
	if len(record.Kinds) > 0 {
		state.Kinds = append([]string(nil), record.Kinds...)
	}
	return state
}

// NotEligibleError reports a refused arm with a stable validation code.
type NotEligibleError struct{ Code string }

func (e NotEligibleError) Error() string { return "automatic reset is not eligible: " + e.Code }

func classify(result ExecutorResult, kind string, now time.Time) *usage.ResetAutomationEvent {
	if result.Unavailable {
		return newEvent(now, "failed", "", ReasonStateUnavailable, kind)
	}
	if result.Ambiguous {
		return newEvent(now, "failed", "", ReasonOutcomeUnknown, kind)
	}
	if result.Outcome == "" {
		return newEvent(now, "failed", "", ReasonRefused, kind)
	}
	return newEvent(now, "completed", completedOutcome(result.Outcome), "", kind)
}

func completedOutcome(outcome string) string {
	switch outcome {
	case "reset", "already_redeemed", "nothing_to_reset", "no_credit", "unavailable":
		return outcome
	default:
		return "unavailable"
	}
}

func (a *Automation) log(message, provider string, err error) {
	if a.logger == nil {
		return
	}
	a.logger.Error(message, slog.String("provider", provider), slog.Any("err", err))
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
