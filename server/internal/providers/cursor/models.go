package cursor

type cursorUsageSummary struct {
	BillingCycleStart *string                `json:"billingCycleStart"`
	IsUnlimited       *bool                  `json:"isUnlimited"`
	LimitType         string                 `json:"limitType"`
	BillingCycleEnd   *string                `json:"billingCycleEnd"`
	MembershipType    *string                `json:"membershipType"`
	IndividualUsage   *cursorIndividualUsage `json:"individualUsage"`
	TeamUsage         *cursorTeamUsage       `json:"teamUsage"`
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
	CurrentPeriodStart      string  `json:"currentPeriodStart"`
	NextResetTimestampUtc   string  `json:"nextResetTimestampUtc"`
	UsagePercent            float64 `json:"usagePercent"`
	HasNonZeroIncludedLimit bool    `json:"hasNonZeroIncludedLimit"`
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
