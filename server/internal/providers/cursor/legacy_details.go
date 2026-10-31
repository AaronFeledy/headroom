package cursor

import (
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"strings"
)

func legacyMeasured(summary cursorUsageSummary, legacy *cursorUsageResponse) bool {
	if _, _, ok := requestUsage(legacy); ok {
		return true
	}
	plan := planUsage(summary)
	positive := func(v *float64) bool { return validPercent(v) && *v > 0 }
	if positive(plan.AutoPercentUsed) || positive(plan.APIPercentUsed) || positive(plan.TotalPercentUsed) || intValue(plan.Limit) > 0 {
		return true
	}
	classic := false
	if summary.MembershipType != nil {
		switch strings.ToLower(*summary.MembershipType) {
		case "free", "pro", "pro_plus", "business", "enterprise":
			classic = true
		}
	}
	if classic && (validPercent(plan.AutoPercentUsed) || validPercent(plan.APIPercentUsed) || validPercent(plan.TotalPercentUsed)) {
		return true
	}
	if summary.IndividualUsage != nil && summary.IndividualUsage.Overall != nil && intValue(summary.IndividualUsage.Overall.Limit) > 0 {
		return true
	}
	return summary.TeamUsage != nil && summary.TeamUsage.Pooled != nil && intValue(summary.TeamUsage.Pooled.Limit) > 0
}

func markLegacyMeasurements(data *usage.UsageData, summary cursorUsageSummary) {
	for i := range data.Buckets {
		b := &data.Buckets[i]
		if b.ID == grokBotBucketID {
			continue
		}
		b.Measurement = usage.MeasurementPercent
		if b.ID == usage.BucketOnDemand {
			personalCap := intValue(onDemandUsage(summary).Limit) > 0
			teamCap := summary.TeamUsage != nil && summary.TeamUsage.OnDemand != nil && intValue(summary.TeamUsage.OnDemand.Limit) > 0
			if !personalCap && !teamCap {
				b.Measurement = usage.MeasurementStatus
			}
		}
	}
}
