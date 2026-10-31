package cursor

import (
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func scopeFromLimit(kind string) *usage.UsageScope {
	switch kind {
	case "user", "individual":
		return &usage.UsageScope{Kind: "personal"}
	case "team", "pooled":
		return &usage.UsageScope{Kind: "team"}
	default:
		return &usage.UsageScope{Kind: "unknown"}
	}
}

func attachDetails(data *usage.UsageData, summary cursorUsageSummary, optional optionalResult, period *currentPeriod, now time.Time) {
	observed := usage.Observation{State: usage.DetailOK, ObservedAt: &now}
	details := &usage.UsageDetails{Plan: &usage.PlanDetails{Observation: observed, IsUnlimited: summary.IsUnlimited, Scope: scopeFromLimit(summary.LimitType)}}
	if summary.MembershipType != nil {
		details.Plan.Label = safeLabel(*summary.MembershipType)
	}
	if label := safeLabel(summary.PlanName); label != nil {
		details.Plan.Label = label
	}
	if optional.plan != nil {
		if label := safeLabel(optional.plan.PlanName); label != nil {
			details.Plan.Label = label
			details.Plan.ObservedAt = &optional.planObserved
			if optional.planStale {
				details.Plan.State = usage.DetailStale
			}
		}
	}
	if details.Plan.Label == nil && details.Plan.IsUnlimited == nil {
		details.Plan.Observation = usage.Observation{State: usage.DetailUnknown}
	}
	if sand := optional.sand; sand != nil {
		sandObservation := usage.Observation{State: usage.DetailOK, ObservedAt: sand.observedAt}
		if sand.stale {
			sandObservation.State = usage.DetailStale
		}
		details.Quotas = []usage.QuotaDetails{{Observation: sandObservation, BucketID: grokBotBucketID, PlanLabel: safeLabel(sand.GrokPlanLabel), HasAvailableUsage: sand.HasAvailableUsage, Scope: &usage.UsageScope{Kind: "unknown", SharedPool: sand.UsesPooledEnterpriseAllowance}}}
	}
	items := periodFinances(period)
	if period == nil {
		items = legacyFinances(summary)
	}
	if len(items) > 0 {
		details.Finances = &usage.FinancialDetails{Observation: observed, Items: items}
	}
	// Summary controls have no proven scope/period compatibility with weekly usage.
	if settings := summary.OnDemandSettings; period == nil && settings != nil && (settings.Enabled != nil || settings.AutoTopUpEnabled != nil) {
		if details.Finances == nil {
			details.Finances = &usage.FinancialDetails{Observation: observed}
		}
		details.Finances.Items = append(details.Finances.Items, usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"}, ExtraEnabled: settings.Enabled, AutoTopUpEnabled: settings.AutoTopUpEnabled})
	}
	// Sand controls are product-local; FinancialItem cannot bind them to Sand.
	// Keep its quota metadata above, without promoting controls to account finances.
	if optional.grants != nil {
		details.CreditGrants = &optional.grants.details
	} else {
		details.CreditGrants = &usage.CreditGrantDetails{Observation: usage.Observation{State: usage.DetailUnknown}}
	}
	data.UsageDetails = details
}

func periodFinances(p *currentPeriod) []usage.FinancialItem {
	if p == nil {
		return nil
	}
	var items []usage.FinancialItem
	if p.Plan != nil {
		plan := p.Plan
		used := plan.IncludedSpend.value
		if used != nil || plan.Limit.value != nil || plan.Remaining.value != nil {
			items = append(items, usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"}, StartsAt: p.Start.value, ResetsAt: p.End.value, IncludedUsedMinor: used, IncludedLimitMinor: plan.Limit.value, IncludedRemainingMinor: plan.Remaining.value})
		}
	}
	if spend := p.Spend; spend != nil {
		if spend.IndividualUsed.value != nil || spend.IndividualLimit.value != nil || spend.IndividualRemaining.value != nil {
			items = append(items, usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "personal"}, StartsAt: p.Start.value, ResetsAt: p.End.value, ExtraUsedMinor: spend.IndividualUsed.value, ExtraCapMinor: spend.IndividualLimit.value})
		}
		if spend.PooledUsed.value != nil || spend.PooledLimit.value != nil || spend.PooledRemaining.value != nil {
			shared := true
			items = append(items, usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "team", SharedPool: &shared}, StartsAt: p.Start.value, ResetsAt: p.End.value, ExtraUsedMinor: spend.PooledUsed.value, ExtraCapMinor: spend.PooledLimit.value})
		}
	}
	return items
}

func minorFromInt(v *int) *int64 {
	if v == nil || *v < 0 || int64(*v) > usage.MaxSafeInteger {
		return nil
	}
	n := int64(*v)
	return &n
}

func legacyFinances(summary cursorUsageSummary) []usage.FinancialItem {
	plan := planUsage(summary)
	onDemand := onDemandUsage(summary)
	item := usage.FinancialItem{Currency: "USD", Scope: scopeFromLimit(summary.LimitType), StartsAt: usage.ValidPeriodStart(parseDate(summary.BillingCycleStart), parseDate(summary.BillingCycleEnd)), ResetsAt: parseDate(summary.BillingCycleEnd), IncludedUsedMinor: minorFromInt(plan.Used), IncludedLimitMinor: minorFromInt(plan.Limit), IncludedRemainingMinor: minorFromInt(plan.Remaining), ExtraUsedMinor: minorFromInt(onDemand.Used), ExtraCapMinor: minorFromInt(onDemand.Limit), ExtraEnabled: onDemand.Enabled}
	if item.IncludedUsedMinor == nil && item.IncludedLimitMinor == nil && item.IncludedRemainingMinor == nil && item.ExtraUsedMinor == nil && item.ExtraCapMinor == nil && item.ExtraEnabled == nil {
		return nil
	}
	return []usage.FinancialItem{item}
}
