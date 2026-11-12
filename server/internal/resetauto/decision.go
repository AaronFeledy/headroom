// Package resetauto owns the server-side, one-shot automatic reset
// authorization. It stores one authorization per provider, evaluates it after
// a completed poll, and spends at most once. Nothing here contacts a provider
// directly: spending is an injected executor so tests can never reach it.
package resetauto

import (
	"math"
	"sort"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"

	// ClaudeArmFloor is the utilization at which Claude arming considers a
	// reading urgent. Codex uses usage.CodexManualResetFloor.
	ClaudeArmFloor = 90

	// maxReadingAge bounds how old a reading may be when it decides to spend.
	// The poller observes only attempts that really fetched, but an observer can
	// be delayed behind a busy process or a slow provider call; an old reading
	// is not evidence that the provider is at its limit right now, so it may
	// cancel but never spend.
	maxReadingAge = 2 * time.Minute
)

// Action is the outcome of evaluating a stored authorization against a reading.
type Action int

const (
	ActionKeep Action = iota
	ActionCancel
	ActionFire
)

// Cancel reason codes reported in last_event.reason.
const (
	ReasonWindowEnded     = "window_ended"
	ReasonIdentityChanged = "identity_changed"
	ReasonKindUnavailable = "kind_unavailable"
	ReasonNoTrigger       = "no_trigger"
	ReasonBelowFloor      = "below_floor"
	ReasonNoCredits       = "no_credits"
)

// Decision is a pure evaluation result. A nonempty Kind is set only for fire.
type Decision struct {
	Action Action
	Reason string
	Kind   string
}

// Record is one provider's stored authorization. A zero ArmedAt means nothing
// is armed; LastEvent survives disarming so a terminal result stays visible.
type Record struct {
	ArmedAt            time.Time
	WindowEndsAt       time.Time
	Kinds              []string
	AccountFingerprint string
	LastEvent          *usage.ResetAutomationEvent
}

func (r Record) Armed() bool { return !r.ArmedAt.IsZero() }

// Decide evaluates one stored authorization against one completed reading. The
// function is pure: the caller persists any cancel and performs any spend.
func Decide(provider string, record Record, entry poller.Entry, now time.Time) Decision {
	if !record.Armed() {
		return Decision{}
	}
	if !now.Before(record.WindowEndsAt) {
		return Decision{Action: ActionCancel, Reason: ReasonWindowEnded}
	}
	if !freshReading(entry, now) {
		return Decision{}
	}
	data := entry.Data
	credits := data.RateLimitResetCredits
	if data.Error != nil || credits == nil || credits.AccountFingerprint == nil {
		return Decision{}
	}
	if *credits.AccountFingerprint != record.AccountFingerprint {
		return Decision{Action: ActionCancel, Reason: ReasonIdentityChanged}
	}
	weekly, ok := realMeter(data, usage.BucketWeekly)
	if !ok || weekly.ResetsAt == nil {
		return Decision{}
	}
	if !sameInstant(*weekly.ResetsAt, record.WindowEndsAt) {
		return Decision{Action: ActionCancel, Reason: ReasonIdentityChanged}
	}
	if provider == ProviderCodex {
		return decideCodex(weekly, credits)
	}
	return decideClaude(data, credits, record)
}

func decideCodex(weekly usage.Bucket, credits *usage.RateLimitResetCredits) Decision {
	utilization := weekly.Utilization
	if !finite(utilization) {
		return Decision{}
	}
	if utilization < usage.CodexManualResetFloor {
		return Decision{Action: ActionCancel, Reason: ReasonBelowFloor}
	}
	if credits.AvailableCount <= 0 {
		return Decision{Action: ActionCancel, Reason: ReasonNoCredits}
	}
	if utilization >= 100 {
		return Decision{Action: ActionFire}
	}
	return Decision{}
}

func decideClaude(data usage.UsageData, credits *usage.RateLimitResetCredits, record Record) Decision {
	if credits.Kinds == nil {
		return Decision{}
	}
	allowed := availableAllowedKinds(record.Kinds, credits)
	if len(allowed) == 0 {
		return Decision{Action: ActionCancel, Reason: ReasonKindUnavailable}
	}
	meters := realMeters(data)
	applies := false
	for _, kind := range allowed {
		if kindAppliesToAny(kind, meters) {
			applies = true
			break
		}
	}
	if !applies {
		return Decision{Action: ActionCancel, Reason: ReasonNoTrigger}
	}
	for _, wall := range walls(data) {
		for _, kind := range preference(allowed, wall) {
			if kind.UsableNow {
				return Decision{Action: ActionFire, Kind: kind.ID}
			}
		}
	}
	return Decision{}
}

// sameInstant compares the second-resolution instant the API carries; provider
// readings may include sub-second precision that JSON serialization drops.
func sameInstant(a, b time.Time) bool {
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}

// freshReading reports whether the entry was fetched recently enough to decide
// a spend. A missing time is never fresh.
func freshReading(entry poller.Entry, now time.Time) bool {
	if entry.FetchedAt.IsZero() {
		return false
	}
	return now.Sub(entry.FetchedAt) <= maxReadingAge
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// realMeters returns the reading's meters in reading order, excluding the
// synthesized session copy of a weekly window.
func realMeters(data usage.UsageData) []usage.Bucket {
	out := make([]usage.Bucket, 0, len(data.Buckets))
	for _, bucket := range data.Buckets {
		if bucket.ID == usage.BucketSession && !data.SessionFromFiveHour {
			continue
		}
		out = append(out, bucket)
	}
	return out
}

func realMeter(data usage.UsageData, id string) (usage.Bucket, bool) {
	for _, bucket := range realMeters(data) {
		if bucket.ID == id {
			return bucket, true
		}
	}
	return usage.Bucket{}, false
}

// walls returns percent meters at or above full utilization, in reading order.
func walls(data usage.UsageData) []usage.Bucket {
	var out []usage.Bucket
	for _, bucket := range realMeters(data) {
		if bucket.Measurement != "" && bucket.Measurement != usage.MeasurementPercent {
			continue
		}
		if finite(bucket.Utilization) && bucket.Utilization >= 100 {
			out = append(out, bucket)
		}
	}
	return out
}

func availableAllowedKinds(allowed []string, credits *usage.RateLimitResetCredits) []usage.ResetCreditKind {
	permitted := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		permitted[id] = true
	}
	out := make([]usage.ResetCreditKind, 0, len(credits.Kinds))
	for _, kind := range credits.Kinds {
		if permitted[kind.ID] && kind.AvailableCount > 0 {
			out = append(out, kind)
		}
	}
	return out
}

func kindAppliesToAny(kind usage.ResetCreditKind, meters []usage.Bucket) bool {
	for _, meter := range meters {
		if kindAppliesTo(kind, meter) {
			return true
		}
	}
	return false
}

func kindAppliesTo(kind usage.ResetCreditKind, meter usage.Bucket) bool {
	for _, refill := range kind.Refills {
		if refill == meter.ID {
			return true
		}
	}
	return meter.ID == usage.BucketWeekly && kind.GivesBackWeekly
}

// preference orders applicable kinds: meters named in refills first, fewer
// refills before more, then kinds that only give back the weekly window. Ties
// keep the server's kind order.
func preference(kinds []usage.ResetCreditKind, wall usage.Bucket) []usage.ResetCreditKind {
	direct := make([]usage.ResetCreditKind, 0, len(kinds))
	weeklyOnly := make([]usage.ResetCreditKind, 0, len(kinds))
	for _, kind := range kinds {
		if contains(kind.Refills, wall.ID) {
			direct = append(direct, kind)
		} else if wall.ID == usage.BucketWeekly && kind.GivesBackWeekly {
			weeklyOnly = append(weeklyOnly, kind)
		}
	}
	sort.SliceStable(direct, func(i, j int) bool { return len(direct[i].Refills) < len(direct[j].Refills) })
	return append(direct, weeklyOnly...)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
