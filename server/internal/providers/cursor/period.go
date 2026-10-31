package cursor

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const currentPeriodPath = "/api/dashboard/get-current-period-usage"
const planInfoPath = "/api/dashboard/get-plan-info"

type currentPeriod struct {
	Start epochMillis  `json:"billingCycleStart"`
	End   epochMillis  `json:"billingCycleEnd"`
	Plan  *periodPlan  `json:"planUsage"`
	Spend *periodSpend `json:"spendLimitUsage"`
}

type periodPlan struct {
	Auto          *float64   `json:"autoPercentUsed"`
	API           *float64   `json:"apiPercentUsed"`
	Total         *float64   `json:"totalPercentUsed"`
	TotalSpend    exactMinor `json:"totalSpend"`
	IncludedSpend exactMinor `json:"includedSpend"`
	BonusSpend    exactMinor `json:"bonusSpend"`
	Remaining     exactMinor `json:"remaining"`
	Limit         exactMinor `json:"limit"`
}

type periodSpend struct {
	IndividualLimit     exactMinor `json:"individualLimit"`
	IndividualUsed      exactMinor `json:"individualUsed"`
	IndividualRemaining exactMinor `json:"individualRemaining"`
	PooledLimit         exactMinor `json:"pooledLimit"`
	PooledUsed          exactMinor `json:"pooledUsed"`
	PooledRemaining     exactMinor `json:"pooledRemaining"`
	LimitType           string     `json:"limitType"`
}

type planInfoResponse struct {
	PlanInfo              *planInfo       `json:"planInfo"`
	IncludedUsagePeriod   json.RawMessage `json:"includedUsagePeriod"`
	IncludedUsageResetsAt epochMillis     `json:"includedUsageResetsAt"`
}

type planInfo struct {
	PlanName              string          `json:"planName"`
	Price                 exactMinor      `json:"price"`
	BillingCycleEnd       epochMillis     `json:"billingCycleEnd"`
	IncludedUsagePeriod   json.RawMessage `json:"includedUsagePeriod"`
	IncludedUsageResetsAt epochMillis     `json:"includedUsageResetsAt"`
}

func (p *planInfo) weekly() bool {
	if p == nil {
		return false
	}
	return string(p.IncludedUsagePeriod) == "2" || string(p.IncludedUsagePeriod) == `"INCLUDED_USAGE_PERIOD_WEEKLY"`
}

func (r planInfoResponse) info() *planInfo {
	p := r.PlanInfo
	if p == nil && len(r.IncludedUsagePeriod) > 0 {
		p = &planInfo{}
	}
	if p == nil {
		return nil
	}
	if len(p.IncludedUsagePeriod) == 0 {
		p.IncludedUsagePeriod = r.IncludedUsagePeriod
	}
	if p.IncludedUsageResetsAt.value == nil {
		p.IncludedUsageResetsAt = r.IncludedUsageResetsAt
	}
	return p
}

func validPercent(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0
}

func (p *currentPeriod) weekly(now time.Time) bool {
	if p == nil || p.Start.value == nil || p.End.value == nil {
		return false
	}
	d := p.End.value.Sub(*p.Start.value)
	return d >= 6*24*time.Hour && d <= 8*24*time.Hour && !now.Before(*p.Start.value) && now.Before(*p.End.value)
}

func (p *currentPeriod) measured() bool {
	return p != nil && p.Plan != nil && (validPercent(p.Plan.Total) || validPercent(p.Plan.Auto) || validPercent(p.Plan.API))
}

func (c *Client) fetchCurrentPeriod(ctx context.Context, cookie string) (*currentPeriod, *usage.FetchFailure) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := c.postJSON(ctx, currentPeriodPath, cookie, []byte("{}"))
	if err != nil {
		return nil, usage.TransportFailure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, usage.HTTPFailure(resp)
	}
	var p currentPeriod
	if decodeOptional(resp.Body, &p) != nil {
		return nil, &usage.FetchFailure{Kind: usage.FailureOther}
	}
	return &p, nil
}

func knownUnified(summary cursorUsageSummary) bool {
	return summary.MembershipType != nil && strings.HasPrefix(strings.ToLower(*summary.MembershipType), "unified_")
}

func populatePeriod(data *usage.UsageData, p *currentPeriod, sand *cursorSandUsage) {
	plan := p.Plan
	buckets := make([]usage.Bucket, 0, 4)
	add := func(id, label string, percent *float64) {
		if validPercent(percent) {
			buckets = append(buckets, usage.Bucket{ID: id, Label: label, Utilization: clampPercent(*percent), StartsAt: p.Start.value, ResetsAt: p.End.value, Measurement: usage.MeasurementPercent})
		}
	}
	if validPercent(plan.Auto) {
		percent := plan.Auto
		if validPercent(plan.Total) {
			percent = plan.Total
		}
		add(usage.BucketAuto, "Cursor Models", percent)
	} else if validPercent(plan.Total) {
		add(usage.BucketPlan, "Included Plan", plan.Total)
	}
	if validPercent(plan.API) && *plan.API > 0 {
		add(usage.BucketAPI, "Other Models", plan.API)
	}
	if len(buckets) == 0 {
		add(usage.BucketAPI, "Other Models", plan.API)
	}
	for i := range buckets {
		if buckets[i].ID == usage.BucketAuto || buckets[i].ID == usage.BucketPlan {
			buckets[i].DetailText = periodMoneyDetail(p)
		}
	}
	if grok, ok := grokBotBucket(sand); ok {
		buckets = append(buckets, grok)
	}
	if b, ok := periodOnDemandBucket(p); ok {
		buckets = append(buckets, b)
	}
	*data = data.WithBuckets(buckets)
	if validPercent(plan.Total) {
		data.Current.Utilization = clampPercent(*plan.Total)
	}
}
