package claude

import (
	"errors"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// Reset kinds mirror Anthropic's own client. Only these four are redeemable.
const (
	resetKindSession = "session"
	resetKindWeekly  = "weekly"
	resetKindFable   = "fable"
	resetKindFull    = "full"
)

var resetKindOrder = []string{resetKindSession, resetKindWeekly, resetKindFable, resetKindFull}

var resetKindLabels = map[string]string{
	resetKindSession: "5-hour reset",
	resetKindWeekly:  "weekly reset",
	resetKindFable:   "Fable reset",
	resetKindFull:    "full reset",
}

var resetRefillOrder = []string{"session", "weekly", "weekly_opus", "weekly_sonnet", "weekly_fable"}

var resetRefillByClear = map[string]string{
	"five_hour":                  "session",
	"seven_day":                  "weekly",
	"seven_day_opus":             "weekly_opus",
	"seven_day_sonnet":           "weekly_sonnet",
	"seven_day_overage_included": "weekly_fable",
}

// knownLimitKeys are the clears values Anthropic's client derives a kind from.
var knownLimitKeys = map[string]bool{
	"five_hour":                  true,
	"seven_day":                  true,
	"seven_day_overage_included": true,
	"seven_day_opus":             true,
	"seven_day_sonnet":           true,
	"seven_day_cowork":           true,
	"seven_day_omelette":         true,
	"seven_day_oauth_apps":       true,
}

var errResetKindAmbiguous = errors.New("multiple reset kinds available")
var errResetKindUnavailable = errors.New("reset kind unavailable")

// recognized reports whether Anthropic's client can map this grant to a kind.
// Unknown types are tolerated and treated as absent so a new upstream type does
// not erase every banked-reset count.
func (g bankGrant) recognized() bool {
	switch g.Type {
	case "", resetKindSession, resetKindWeekly, resetKindFull:
		return true
	default:
		return false
	}
}

// kind classifies one grant exactly like Anthropic's client: an explicit type
// when recognized, otherwise the limit keys it clears.
func (g bankGrant) kind() string {
	if g.GivesBackWeekly {
		return resetKindFable
	}
	if g.recognized() && g.Type != "" {
		return g.Type
	}
	hasFiveHour := false
	hasOther := false
	for _, clear := range g.Clears {
		if clear == "five_hour" {
			hasFiveHour = true
			continue
		}
		if knownLimitKeys[clear] {
			hasOther = true
		}
	}
	switch {
	case hasFiveHour && !hasOther:
		return resetKindSession
	case hasOther && !hasFiveHour:
		return resetKindWeekly
	default:
		return resetKindFull
	}
}

func (g bankGrant) counted() bool {
	return !*g.Paused && *g.ResetsLeft > 0
}

func (g bankGrant) windowOpen(now time.Time) bool {
	if g.StartsAt != nil {
		start, err := time.Parse(time.RFC3339, *g.StartsAt)
		if err != nil || start.After(now) {
			return false
		}
	}
	if g.EndsAt != nil {
		end, err := time.Parse(time.RFC3339, *g.EndsAt)
		if err != nil || !end.After(now) {
			return false
		}
	}
	return true
}

type kindAccumulator struct {
	available int64
	usable    bool
	expires   *time.Time
	refills   map[string]bool
}

func (s *bankStatus) usableAt(now time.Time) bool {
	return s.eligible && (s.cooldownUntil == nil || !s.cooldownUntil.After(now))
}

// countKind sums the counted grants of one kind; it powers the single-kind rule.
func (s *bankStatus) countKind(kind string) int64 {
	var total int64
	for _, grant := range s.grants {
		if grant.counted() && grant.kind() == kind {
			total += *grant.ResetsLeft
		}
	}
	return total
}

func (s *bankStatus) credits(now time.Time) *usage.RateLimitResetCredits {
	if s == nil {
		return nil
	}
	var available int64
	usable := false
	accumulated := make(map[string]*kindAccumulator, len(resetKindOrder))
	for _, grant := range s.grants {
		if *grant.ResetsLeft <= 0 {
			continue
		}
		if *grant.Paused {
			continue
		}
		available += *grant.ResetsLeft
		if grant.recognized() && grant.ID == s.nextGrantID && *grant.UsableNow {
			usable = true
		}
		kind := grant.kind()
		accumulator := accumulated[kind]
		if accumulator == nil {
			accumulator = &kindAccumulator{refills: map[string]bool{}}
			accumulated[kind] = accumulator
		}
		accumulator.available += *grant.ResetsLeft
		if grant.recognized() && *grant.UsableNow {
			accumulator.usable = true
		}
		if grant.EndsAt != nil {
			if parsed, err := time.Parse(time.RFC3339, *grant.EndsAt); err == nil {
				if accumulator.expires == nil || parsed.Before(*accumulator.expires) {
					expiry := parsed
					accumulator.expires = &expiry
				}
			}
		}
		for _, clear := range grant.Clears {
			if refill, ok := resetRefillByClear[clear]; ok {
				accumulator.refills[refill] = true
			}
		}
	}
	// A validated empty/ineligible reading is known zero, not unknown. Keep
	// it serializable so an account-bound desktop receipt can be reconciled.
	usable = usable && s.usableAt(now)
	kinds := make([]usage.ResetCreditKind, 0, len(resetKindOrder))
	for _, id := range resetKindOrder {
		accumulator := accumulated[id]
		if accumulator == nil {
			continue
		}
		var expires *string
		if accumulator.expires != nil {
			formatted := accumulator.expires.UTC().Format(time.RFC3339)
			expires = &formatted
		}
		refills := make([]string, 0, len(resetRefillOrder))
		for _, refill := range resetRefillOrder {
			if accumulator.refills[refill] {
				refills = append(refills, refill)
			}
		}
		entry := usage.ResetCreditKind{
			ID:              id,
			Label:           resetKindLabels[id],
			AvailableCount:  accumulator.available,
			UsableNow:       accumulator.usable && s.usableAt(now),
			Refills:         refills,
			GivesBackWeekly: id == resetKindFable,
			ExpiresAt:       expires,
		}
		kinds = append(kinds, entry)
	}
	return &usage.RateLimitResetCredits{AvailableCount: available, UsableNow: &usable, Kinds: kinds}
}

// selectResetGrant is the pure pre-submission choice over a parsed status.
// Kind-aware redemption requires an open, counted, usable, recognized grant of
// that kind. Legacy redemption keeps next_grant_id, but only when at most one
// kind has resets available; an unrecognized next grant is always a refusal.
func selectResetGrant(status *bankStatus, kind string, now time.Time) (*bankGrant, error) {
	if status == nil {
		return nil, errResetUnavailable
	}
	if kind == "" {
		kindsWithResets := 0
		for _, id := range resetKindOrder {
			if status.countKind(id) > 0 {
				kindsWithResets++
			}
		}
		if kindsWithResets > 1 {
			return nil, errResetKindAmbiguous
		}
		for index := range status.grants {
			grant := &status.grants[index]
			if grant.ID != status.nextGrantID {
				continue
			}
			if !grant.recognized() {
				return nil, errResetUnavailable
			}
			return grant, nil
		}
		return nil, errResetUnavailable
	}
	var candidates []*bankGrant
	for index := range status.grants {
		grant := &status.grants[index]
		if !grant.recognized() || grant.kind() != kind || !grant.counted() || !*grant.UsableNow || !grant.windowOpen(now) {
			continue
		}
		candidates = append(candidates, grant)
	}
	if len(candidates) == 0 {
		return nil, errResetKindUnavailable
	}
	for _, candidate := range candidates {
		if candidate.ID == status.nextGrantID {
			return candidate, nil
		}
	}
	return candidates[0], nil
}
