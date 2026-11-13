package usage

import "math"

// MaxBuckets caps how many meters a provider may emit (tray height safety).
const MaxBuckets = 12

// Standard bucket IDs shared across providers.
const (
	BucketSession  = "session"
	BucketWeekly   = "weekly"
	BucketPlan     = "plan"
	BucketAuto     = "auto"
	BucketAPI      = "api"
	BucketCredits  = "credits"
	BucketExtra    = "extra"
	BucketOnDemand = "on_demand"
	BucketMonthly  = "monthly"
)

// FromBuckets is the preferred provider exit path: set provider name and
// normalized meters, deriving current/weekly/labels via WithBuckets.
func FromBuckets(providerName string, buckets []Bucket) UsageData {
	return UsageData{ProviderName: providerName}.WithBuckets(buckets)
}

// NormalizeBuckets orders meters consistently, drops empty IDs, dedupes by ID
// (first wins), and caps length at MaxBuckets.
//
// Order: session-like (session/plan/auto/api/credits) → weekly → weekly_* → credit-like
// (extra/on_demand) → everything else (stable relative order within groups).
func NormalizeBuckets(buckets []Bucket) []Bucket {
	if len(buckets) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(buckets))
	groups := [5][]Bucket{}
	for _, bucket := range buckets {
		if bucket.ID == "" {
			continue
		}
		if _, ok := seen[bucket.ID]; ok {
			continue
		}
		seen[bucket.ID] = struct{}{}
		groups[bucketGroup(bucket.ID)] = append(groups[bucketGroup(bucket.ID)], bucket)
	}
	out := make([]Bucket, 0, len(buckets))
	for _, group := range groups {
		out = append(out, group...)
	}
	if len(out) > MaxBuckets {
		out = out[:MaxBuckets]
	}
	return out
}

func bucketGroup(id string) int {
	switch {
	case id == BucketSession || id == BucketPlan || id == BucketAuto || id == BucketAPI || id == BucketCredits:
		return 0
	case id == BucketWeekly:
		return 1
	case len(id) > 7 && id[:7] == "weekly_":
		return 2
	case id == BucketExtra || id == BucketOnDemand:
		return 3
	default:
		return 4
	}
}

// ShouldShowCreditMeter reports whether a billable/credit meter should appear.
// Show when the account has the feature enabled, or when there is non-zero
// usage (spent credits or positive utilization).
func ShouldShowCreditMeter(enabled bool, usedCredits float64, utilization *float64) bool {
	if enabled {
		return true
	}
	if usedCredits > 0 {
		return true
	}
	return utilization != nil && *utilization > 0
}

// CreditUtilization returns a 0–100 percent for a credit meter.
// Prefer explicit utilization; otherwise used/limit when limit > 0; else 0.
func CreditUtilization(usedCredits, monthlyLimit float64, utilization *float64) float64 {
	if utilization != nil {
		return clampUtilization(*utilization)
	}
	if monthlyLimit > 0 {
		return clampUtilization(usedCredits / monthlyLimit * 100)
	}
	return 0
}

func clampUtilization(value float64) float64 {
	return math.Min(math.Max(value, 0), 100)
}
