package cursor

import (
	"encoding/json"
	"fmt"
	"time"
)

type cursorUsageSummary struct {
	BillingCycleStart *string                `json:"billingCycleStart"`
	IsUnlimited       *bool                  `json:"isUnlimited"`
	LimitType         string                 `json:"limitType"`
	BillingCycleEnd   *string                `json:"billingCycleEnd"`
	MembershipType    *string                `json:"membershipType"`
	IndividualUsage   *cursorIndividualUsage `json:"individualUsage"`
	TeamUsage         *cursorTeamUsage       `json:"teamUsage"`
	PlanName          string                 `json:"planName"`
	BillingBrand      string                 `json:"billingBrand"`
	OnDemandSettings  *onDemandSettings      `json:"onDemandSettings"`
}

func (s *cursorUsageSummary) UnmarshalJSON(body []byte) error {
	type summaryAlias cursorUsageSummary
	var parsed summaryAlias
	wire := struct {
		*summaryAlias
		PlanName         json.RawMessage `json:"planName"`
		BillingBrand     json.RawMessage `json:"billingBrand"`
		OnDemandSettings json.RawMessage `json:"onDemandSettings"`
	}{summaryAlias: &parsed}
	if err := json.Unmarshal(body, &wire); err != nil {
		return fmt.Errorf("decode core summary: %w", err)
	}
	if err := json.Unmarshal(wire.PlanName, &parsed.PlanName); err != nil {
		parsed.PlanName = ""
	}
	if err := json.Unmarshal(wire.BillingBrand, &parsed.BillingBrand); err != nil {
		parsed.BillingBrand = ""
	}
	if err := json.Unmarshal(wire.OnDemandSettings, &parsed.OnDemandSettings); err != nil {
		parsed.OnDemandSettings = nil
	}
	*s = cursorUsageSummary(parsed)
	return nil
}

type cursorIndividualUsage struct {
	Plan     *cursorPlanUsage     `json:"plan"`
	OnDemand *cursorOnDemandUsage `json:"onDemand"`
	Overall  *cursorOverallUsage  `json:"overall"`
}

type cursorPlanUsage struct {
	Remaining        *int                 `json:"remaining"`
	Breakdown        *cursorPlanBreakdown `json:"breakdown"`
	Used             *int                 `json:"used"`
	Limit            *int                 `json:"limit"`
	AutoPercentUsed  *float64             `json:"autoPercentUsed"`
	APIPercentUsed   *float64             `json:"apiPercentUsed"`
	TotalPercentUsed *float64             `json:"totalPercentUsed"`
}

type cursorOverallUsage struct {
	Enabled   *bool `json:"enabled"`
	Used      *int  `json:"used"`
	Limit     *int  `json:"limit"`
	Remaining *int  `json:"remaining"`
}

type cursorOnDemandUsage struct {
	Enabled *bool `json:"enabled"`
	Used    *int  `json:"used"`
	Limit   *int  `json:"limit"`
}

type cursorTeamUsage struct {
	OnDemand *cursorOnDemandUsage `json:"onDemand"`
	Pooled   *cursorPooledUsage   `json:"pooled"`
}

type cursorPooledUsage struct {
	Enabled   *bool `json:"enabled"`
	Used      *int  `json:"used"`
	Limit     *int  `json:"limit"`
	Remaining *int  `json:"remaining"`
}

type cursorSandUsage struct {
	CurrentPeriodStart            string            `json:"currentPeriodStart"`
	NextResetTimestampUtc         string            `json:"nextResetTimestampUtc"`
	UsagePercent                  float64           `json:"usagePercent"`
	HasNonZeroIncludedLimit       bool              `json:"hasNonZeroIncludedLimit"`
	HasAvailableUsage             *bool             `json:"hasAvailableUsage"`
	UsesPooledEnterpriseAllowance *bool             `json:"usesPooledEnterpriseAllowance"`
	GrokPlanLabel                 string            `json:"grokPlanLabel"`
	OnDemandSettings              *onDemandSettings `json:"onDemandSettings"`
	percentReported               bool
	observedAt                    *time.Time
	stale                         bool
}

type onDemandSettings struct {
	Enabled          *bool `json:"enabled"`
	AutoTopUpEnabled *bool `json:"autoTopUpEnabled"`
}

type cursorUserInfo struct {
	Sub string `json:"sub"`
}

type cursorUsageResponse struct {
	GPT4 *cursorModelUsage `json:"gpt-4"`
}

type cursorModelUsage struct {
	NumRequests      *int `json:"numRequests"`
	NumRequestsTotal *int `json:"numRequestsTotal"`
	MaxRequestUsage  *int `json:"maxRequestUsage"`
}

type cursorPlanBreakdown struct {
	Included *int `json:"included"`
	Bonus    *int `json:"bonus"`
	Total    *int `json:"total"`
}
