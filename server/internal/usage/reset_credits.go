package usage

import "encoding/json"

// ResetCreditKind is one class of banked reset with only its counted grants.
type ResetCreditKind struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	AvailableCount  int64    `json:"available_count"`
	UsableNow       bool     `json:"usable_now"`
	Refills         []string `json:"refills"`
	GivesBackWeekly bool     `json:"gives_back_weekly"`
	ExpiresAt       *string  `json:"expires_at"`
	// Next describes the grant a kind redemption would spend now, or nil.
	Next *ResetCreditNext `json:"next"`
}

// ResetCreditNext is the upstream grant wording a kind would spend next.
type ResetCreditNext struct {
	Label     string  `json:"label"`
	ExpiresAt *string `json:"expires_at"`
}

// Kind returns the listed kind with this identifier, or nil when absent.
func (c *RateLimitResetCredits) Kind(id string) *ResetCreditKind {
	if c == nil {
		return nil
	}
	for index := range c.Kinds {
		if c.Kinds[index].ID == id {
			return &c.Kinds[index]
		}
	}
	return nil
}

// AvailableKindCount counts listed kinds that still have resets available.
func (c *RateLimitResetCredits) AvailableKindCount() int {
	if c == nil {
		return 0
	}
	count := 0
	for _, kind := range c.Kinds {
		if kind.AvailableCount > 0 {
			count++
		}
	}
	return count
}

func (credits RateLimitResetCredits) MarshalJSON() ([]byte, error) {
	type fields RateLimitResetCredits
	var ids []string
	if credits.ReconciledRequests != nil {
		ids = credits.ReconciledRequests()
	}
	return json.Marshal(struct {
		fields
		ReconciledRequestIDs []string `json:"reconciled_request_ids,omitempty"`
	}{fields: fields(credits), ReconciledRequestIDs: ids})
}
