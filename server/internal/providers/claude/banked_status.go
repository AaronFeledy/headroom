package claude

import (
	"encoding/json"
	"math"
	"regexp"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

var grantIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type bankGrant struct {
	ID                string         `json:"id"`
	Label             string         `json:"label"`
	ResetsTotal       *int64         `json:"resets_total"`
	ResetsLeft        *int64         `json:"resets_left"`
	Paused            *bool          `json:"paused"`
	UsableNow         *bool          `json:"usable_now"`
	StartsAt          *string        `json:"starts_at"`
	EndsAt            *string        `json:"ends_at"`
	Clears            []string       `json:"clears"`
	UseRequiresLimit  bool           `json:"use_requires_limit"`
	PercentUsed       map[string]int `json:"percent_used"`
	Blocking          []string       `json:"blocking"`
	Type              string         `json:"type"`
	GivesBackWeekly   bool           `json:"gives_back_weekly"`
	UnavailableReason *string        `json:"unavailable_reason"`
}

type bankStatus struct {
	eligible      bool
	nextGrantID   string
	cooldownUntil *time.Time
	grants        []bankGrant
}

func parseBankStatus(body []byte) *bankStatus {
	var envelope struct {
		CedarEmber json.RawMessage `json:"cedar_ember"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	var block struct {
		Eligible         *bool             `json:"eligible"`
		IneligibleReason *string           `json:"ineligible_reason"`
		AtLimit          *bool             `json:"at_limit"`
		Exhausted        []string          `json:"exhausted"`
		WeeklyResetsAt   *string           `json:"weekly_resets_at"`
		NextGrantID      *string           `json:"next_grant_id"`
		CooldownUntil    *time.Time        `json:"cooldown_until"`
		Grants           []json.RawMessage `json:"grants"`
	}
	if json.Unmarshal(envelope.CedarEmber, &block) != nil || block.Eligible == nil || block.Grants == nil {
		return nil
	}
	status := &bankStatus{eligible: *block.Eligible, cooldownUntil: block.CooldownUntil}
	if block.NextGrantID != nil {
		if !grantIDPattern.MatchString(*block.NextGrantID) {
			return nil
		}
		status.nextGrantID = *block.NextGrantID
	}
	seen := make(map[string]bool)
	var total int64
	for _, raw := range block.Grants {
		var grant bankGrant
		// A partial count or inferred refusal could release an unresolved reset.
		// Invalid grants make the whole reading unknown; never discard them.
		if json.Unmarshal(raw, &grant) != nil || !grantIDPattern.MatchString(grant.ID) || grant.ResetsTotal == nil || grant.ResetsLeft == nil || *grant.ResetsTotal < 0 || *grant.ResetsLeft < 0 || grant.Paused == nil || grant.UsableNow == nil || seen[grant.ID] {
			return nil
		}
		seen[grant.ID] = true
		switch grant.Type {
		case "", "session", "weekly", "full":
		default:
			return nil
		}
		if status.nextGrantID == "" && !*grant.Paused && *grant.ResetsLeft > 0 && *grant.UsableNow {
			return nil
		}
		if !*grant.Paused {
			if *grant.ResetsLeft > math.MaxInt64-total {
				return nil
			}
			total += *grant.ResetsLeft
		}
		status.grants = append(status.grants, grant)
	}
	if status.nextGrantID != "" && !seen[status.nextGrantID] {
		return nil
	}
	return status
}

func (s *bankStatus) credits(now time.Time) *usage.RateLimitResetCredits {
	if s == nil {
		return nil
	}
	var available int64
	usable := false
	for _, grant := range s.grants {
		if *grant.ResetsLeft <= 0 {
			continue
		}
		if *grant.Paused {
			continue
		}
		available += *grant.ResetsLeft
		if grant.ID == s.nextGrantID && *grant.UsableNow {
			usable = true
		}
	}
	// A validated empty/ineligible reading is known zero, not unknown. Keep
	// it serializable so an account-bound desktop receipt can be reconciled.
	usable = usable && s.eligible && (s.cooldownUntil == nil || !s.cooldownUntil.After(now))
	return &usage.RateLimitResetCredits{AvailableCount: available, UsableNow: &usable}
}
