package cursor

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func populateUsageData(data *usage.UsageData, summary cursorUsageSummary, legacyUsage *cursorUsageResponse, sand *cursorSandUsage) {
	billingCycleEnd := parseDate(summary.BillingCycleEnd)
	billingCycleStart := usage.ValidPeriodStart(parseDate(summary.BillingCycleStart), billingCycleEnd)
	planUsedRaw := intValue(planUsage(summary).Used)
	planLimitRaw := intValue(planUsage(summary).Limit)
	planPercent := percentFromPlan(summary, planUsedRaw, planLimitRaw)
	statusUsedRaw, statusLimitRaw := headlineMoneyUsage(summary, planUsedRaw, planLimitRaw)

	requestsUsed, requestsLimit, hasRequests := requestUsage(legacyUsage)
	if hasRequests {
		data.PrimaryLabel = "Requests"
		planPercent = float64(requestsUsed) / float64(requestsLimit) * 100
		primaryStatus := fmt.Sprintf("%d / %d requests this cycle", requestsUsed, requestsLimit)
		data.PrimaryStatusText = &primaryStatus
	} else {
		primaryStatus := fmt.Sprintf("$%s / $%s this cycle", money(statusUsedRaw), money(statusLimitRaw))
		data.PrimaryStatusText = &primaryStatus
	}

	data.Current = usage.UsageBucket{Utilization: clampPercent(planPercent), ResetsAt: billingCycleEnd}
	onDemandBucket, onDemandStatus, showOnDemand := resolveOnDemandBucket(summary, billingCycleEnd)
	if showOnDemand {
		data.Weekly = usage.UsageBucket{Utilization: onDemandBucket.Utilization, ResetsAt: onDemandBucket.ResetsAt}
		data.SecondaryStatusText = onDemandStatus
	} else {
		data.ShowSecondary = false
		data.SecondaryStatusText = onDemandStatus
	}

	secondaryLabel := data.SecondaryLabel
	primaryStatus := data.PrimaryStatusText
	secondaryStatus := data.SecondaryStatusText
	legacyCurrent := data.Current
	legacyWeekly := data.Weekly
	legacyPrimaryLabel := data.PrimaryLabel
	legacyShowSecondary := data.ShowSecondary
	plan := planUsage(summary)
	separatePlanBuckets := plan.AutoPercentUsed != nil || plan.APIPercentUsed != nil
	buckets := make([]usage.Bucket, 0, 4)
	if plan.AutoPercentUsed != nil {
		buckets = append(buckets, usage.Bucket{
			ID:          usage.BucketAuto,
			Label:       "Cursor Models",
			Utilization: clampPercent(*plan.AutoPercentUsed),
			ResetsAt:    billingCycleEnd,
		})
	}
	if plan.APIPercentUsed != nil {
		buckets = append(buckets, usage.Bucket{
			ID:          usage.BucketAPI,
			Label:       "Other Models",
			Utilization: clampPercent(*plan.APIPercentUsed),
			ResetsAt:    billingCycleEnd,
			StatusText:  primaryStatus,
		})
	}
	if !separatePlanBuckets {
		buckets = append(buckets, usage.Bucket{
			ID:          usage.BucketPlan,
			Label:       data.PrimaryLabel,
			Utilization: data.Current.Utilization,
			ResetsAt:    data.Current.ResetsAt,
			StatusText:  primaryStatus,
		})
	}
	if grok, ok := grokBotBucket(sand); ok {
		buckets = append(buckets, grok)
	}
	if showOnDemand {
		buckets = append(buckets, onDemandBucket)
	}
	for i := range buckets {
		if buckets[i].ID == grokBotBucketID {
			continue
		}
		buckets[i].StartsAt = billingCycleStart
		if buckets[i].ID != usage.BucketOnDemand {
			buckets[i].DetailText = planDetails(summary)
		}
	}
	*data = data.WithBuckets(buckets)
	data.Current = legacyCurrent
	data.Weekly = legacyWeekly
	data.PrimaryLabel = legacyPrimaryLabel
	data.SecondaryLabel = secondaryLabel
	data.ShowSecondary = legacyShowSecondary
	data.PrimaryStatusText = primaryStatus
	data.SecondaryStatusText = secondaryStatus
}

func resolveOnDemandBucket(summary cursorUsageSummary, billingCycleEnd *time.Time) (usage.Bucket, *string, bool) {
	onDemand := onDemandUsage(summary)
	onDemandUsedRaw := intValue(onDemand.Used)
	enabled := onDemand.Enabled != nil && *onDemand.Enabled
	if onDemand.Limit != nil && *onDemand.Limit > 0 {
		limit := *onDemand.Limit
		status := fmt.Sprintf("$%s / $%s on-demand", money(onDemandUsedRaw), money(limit))
		return usage.Bucket{
			ID:          usage.BucketOnDemand,
			Label:       "On-Demand",
			Utilization: clampPercent(float64(onDemandUsedRaw) / float64(limit) * 100),
			ResetsAt:    billingCycleEnd,
			StatusText:  &status,
		}, &status, true
	}
	if summary.TeamUsage != nil && summary.TeamUsage.OnDemand != nil {
		teamOnDemand := *summary.TeamUsage.OnDemand
		if teamOnDemand.Limit != nil && *teamOnDemand.Limit > 0 {
			teamUsed := intValue(teamOnDemand.Used)
			teamLimit := *teamOnDemand.Limit
			status := fmt.Sprintf("$%s / $%s team on-demand", money(teamUsed), money(teamLimit))
			return usage.Bucket{
				ID:          usage.BucketOnDemand,
				Label:       "On-Demand",
				Utilization: clampPercent(float64(teamUsed) / float64(teamLimit) * 100),
				ResetsAt:    billingCycleEnd,
				StatusText:  &status,
			}, &status, true
		}
	}
	if usage.ShouldShowCreditMeter(enabled, float64(onDemandUsedRaw), nil) {
		status := fmt.Sprintf("$%s on-demand this cycle", money(onDemandUsedRaw))
		if enabled && onDemandUsedRaw == 0 {
			status = "On-demand enabled"
		}
		return usage.Bucket{
			ID:          usage.BucketOnDemand,
			Label:       "On-Demand",
			Utilization: 0,
			ResetsAt:    billingCycleEnd,
			StatusText:  &status,
		}, &status, true
	}
	status := "No on-demand cap exposed by Cursor"
	return usage.Bucket{}, &status, false
}

func planUsage(summary cursorUsageSummary) cursorPlanUsage {
	if summary.IndividualUsage == nil || summary.IndividualUsage.Plan == nil {
		return cursorPlanUsage{}
	}
	return *summary.IndividualUsage.Plan
}

func onDemandUsage(summary cursorUsageSummary) cursorOnDemandUsage {
	if summary.IndividualUsage == nil || summary.IndividualUsage.OnDemand == nil {
		return cursorOnDemandUsage{}
	}
	return *summary.IndividualUsage.OnDemand
}

func requestUsage(legacyUsage *cursorUsageResponse) (int, int, bool) {
	if legacyUsage == nil || legacyUsage.GPT4 == nil || legacyUsage.GPT4.MaxRequestUsage == nil {
		return 0, 0, false
	}
	requestsLimit := *legacyUsage.GPT4.MaxRequestUsage
	if requestsLimit <= 0 {
		return 0, 0, false
	}
	if legacyUsage.GPT4.NumRequestsTotal != nil {
		return *legacyUsage.GPT4.NumRequestsTotal, requestsLimit, true
	}
	if legacyUsage.GPT4.NumRequests != nil {
		return *legacyUsage.GPT4.NumRequests, requestsLimit, true
	}
	return 0, 0, false
}

func percentFromPlan(summary cursorUsageSummary, planUsedRaw int, planLimitRaw int) float64 {
	plan := planUsage(summary)
	if plan.TotalPercentUsed != nil {
		return clampPercent(*plan.TotalPercentUsed)
	}
	if plan.AutoPercentUsed != nil && plan.APIPercentUsed != nil {
		return (clampPercent(*plan.AutoPercentUsed) + clampPercent(*plan.APIPercentUsed)) / 2
	}
	if plan.APIPercentUsed != nil {
		return clampPercent(*plan.APIPercentUsed)
	}
	if plan.AutoPercentUsed != nil {
		return clampPercent(*plan.AutoPercentUsed)
	}
	if planLimitRaw > 0 {
		return float64(planUsedRaw) / float64(planLimitRaw) * 100
	}
	if summary.IndividualUsage != nil && summary.IndividualUsage.Overall != nil {
		overall := summary.IndividualUsage.Overall
		limit := intValue(overall.Limit)
		if limit > 0 {
			return float64(intValue(overall.Used)) / float64(limit) * 100
		}
	}
	if summary.TeamUsage != nil && summary.TeamUsage.Pooled != nil {
		pooled := summary.TeamUsage.Pooled
		limit := intValue(pooled.Limit)
		if limit > 0 {
			return float64(intValue(pooled.Used)) / float64(limit) * 100
		}
	}
	return 0
}

func headlineMoneyUsage(summary cursorUsageSummary, planUsedRaw int, planLimitRaw int) (int, int) {
	if planUsedRaw > 0 || planLimitRaw > 0 {
		return planUsedRaw, planLimitRaw
	}
	if summary.IndividualUsage != nil && summary.IndividualUsage.Overall != nil {
		overall := summary.IndividualUsage.Overall
		used, limit := intValue(overall.Used), intValue(overall.Limit)
		if used > 0 || limit > 0 {
			return used, limit
		}
	}
	if summary.TeamUsage != nil && summary.TeamUsage.Pooled != nil {
		return intValue(summary.TeamUsage.Pooled.Used), intValue(summary.TeamUsage.Pooled.Limit)
	}
	return 0, 0
}

func parseDate(value *string) *time.Time {
	if value == nil || *value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func clampPercent(value float64) float64 {
	return math.Min(math.Max(value, 0), 100)
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func money(cents int) string {
	dollars := float64(cents) / 100
	formatted := strconv.FormatFloat(dollars, 'f', 2, 64)
	return strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
}

// Plan totals cover the whole plan, not either individual model pool.
func planDetails(summary cursorUsageSummary) *string {
	lines := []string{}
	plan := planUsage(summary)
	addMoney := func(label string, value *int) {
		if value != nil && *value >= 0 {
			lines = append(lines, label+": $"+money(*value))
		}
	}
	addMoney("Plan remaining", plan.Remaining)
	if plan.Breakdown != nil {
		addMoney("Included usage", plan.Breakdown.Included)
		addMoney("Bonus usage", plan.Breakdown.Bonus)
		addMoney("Total plan usage", plan.Breakdown.Total)
	}
	if len(lines) > 0 {
		lines = append([]string{"Plan-wide amounts reported by Cursor:"}, lines...)
	}
	if summary.IsUnlimited != nil && *summary.IsUnlimited {
		lines = append(lines, "Cursor reports unlimited access; model-specific limits may still apply.")
	}
	switch summary.LimitType {
	case "user":
		lines = append(lines, "Limit scope: individual")
	case "team":
		lines = append(lines, "Limit scope: team")
	}
	if len(lines) == 0 {
		return nil
	}
	detail := strings.Join(lines, "\n")
	return &detail
}
