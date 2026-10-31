package cursor

import (
	"fmt"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func exactDollars(cents int64) string {
	s := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

func periodMoneyDetail(p *currentPeriod) *string {
	var lines []string
	add := func(label string, value *int64) {
		if value != nil {
			lines = append(lines, label+": $"+exactDollars(*value))
		}
	}
	add("Total plan spend", p.Plan.TotalSpend.value)
	add("Included spend", p.Plan.IncludedSpend.value)
	add("Bonus spend", p.Plan.BonusSpend.value)
	add("Plan remaining", p.Plan.Remaining.value)
	add("Plan limit", p.Plan.Limit.value)
	if len(lines) == 0 {
		return nil
	}
	text := strings.Join(lines, "\n")
	return &text
}

func periodOnDemandBucket(p *currentPeriod) (usage.Bucket, bool) {
	if p.Spend == nil {
		return usage.Bucket{}, false
	}
	spend := p.Spend
	used, limit, remaining := spend.IndividualUsed.value, spend.IndividualLimit.value, spend.IndividualRemaining.value
	if spend.LimitType == "team" || ((limit == nil || *limit == 0) && spend.PooledLimit.value != nil && *spend.PooledLimit.value > 0) {
		used, limit, remaining = spend.PooledUsed.value, spend.PooledLimit.value, spend.PooledRemaining.value
	}
	if (limit == nil || *limit == 0) && (used == nil || *used == 0) {
		return usage.Bucket{}, false
	}
	status := "On-demand budget reported"
	b := usage.Bucket{ID: usage.BucketOnDemand, Label: "On-Demand", StartsAt: p.Start.value, ResetsAt: p.End.value, Measurement: usage.MeasurementStatus}
	if used != nil {
		status = "$" + exactDollars(*used) + " on-demand this period"
		if limit != nil && *limit > 0 {
			status = "$" + exactDollars(*used) + " / $" + exactDollars(*limit) + " on-demand"
			b.Utilization = clampPercent(float64(*used) / float64(*limit) * 100)
			b.Measurement = usage.MeasurementPercent
		}
	}
	if remaining != nil {
		detail := "On-demand remaining: $" + exactDollars(*remaining)
		b.DetailText = &detail
	}
	b.StatusText = &status
	return b, true
}
