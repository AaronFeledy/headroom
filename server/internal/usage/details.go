package usage

import "time"

// Provider adapters enforce these bounds before constructing details. Monetary
// values are exact minor units, never rounded from fractional provider credits.
const (
	MaxDetailItems       = 64
	MaxSafeInteger int64 = 9007199254740991
)

const (
	DetailOK          = "ok"
	DetailStale       = "stale"
	DetailUnknown     = "unknown"
	DetailUnsupported = "unsupported"
)

const (
	MeasurementPercent = "percent"
	MeasurementStatus  = "status"
)

// UsageDetails is additive, read-only metadata, independent of percent buckets
// and reset actions. Nil sections/slices mean unknown; empty slices mean known
// empty results. Adapters own validation and display-string sanitization.
type UsageDetails struct {
	Plan           *PlanDetails           `json:"plan"`
	Quotas         []QuotaDetails         `json:"quotas"`
	Finances       *FinancialDetails      `json:"finances"`
	CreditGrants   *CreditGrantDetails    `json:"credit_grants"`
	ResetInventory *ResetInventoryDetails `json:"reset_inventory"`
	ProductUsage   *ProductUsageDetails   `json:"product_usage"`
}

type Observation struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
}

// UsageScope describes only its containing row, not cross-provider identity.
// Kind is "unknown", "personal", or "team", as explicitly reported upstream.
type UsageScope struct {
	Kind       string `json:"kind"`
	SharedPool *bool  `json:"shared_pool"`
}

type PlanDetails struct {
	Observation
	Label       *string     `json:"label"`
	IsUnlimited *bool       `json:"is_unlimited"`
	Scope       *UsageScope `json:"scope"`
}

type QuotaDetails struct {
	Observation
	BucketID          string      `json:"bucket_id"`
	PlanLabel         *string     `json:"plan_label"`
	HasAvailableUsage *bool       `json:"has_available_usage"`
	Scope             *UsageScope `json:"scope"`
}

type FinancialDetails struct {
	Observation
	Items []FinancialItem `json:"items"`
}

type FinancialItem struct {
	Currency               string      `json:"currency"`
	Scope                  *UsageScope `json:"scope"`
	StartsAt               *time.Time  `json:"starts_at"`
	ResetsAt               *time.Time  `json:"resets_at"`
	IncludedUsedMinor      *int64      `json:"included_used_minor"`
	IncludedLimitMinor     *int64      `json:"included_limit_minor"`
	IncludedRemainingMinor *int64      `json:"included_remaining_minor"`
	PrepaidRemainingMinor  *int64      `json:"prepaid_remaining_minor"`
	ExtraUsedMinor         *int64      `json:"extra_used_minor"`
	ExtraCapMinor          *int64      `json:"extra_cap_minor"`
	ExtraEnabled           *bool       `json:"extra_enabled"`
	AutoTopUpEnabled       *bool       `json:"auto_top_up_enabled"`
}

type CreditGrantDetails struct {
	Observation
	Items []CreditGrant `json:"items"`
}

type CreditGrant struct {
	// DisplayName is sanitized cosmetic text, never a source of scope/product identity.
	DisplayName    string      `json:"display_name"`
	Product        *string     `json:"product"`
	Currency       string      `json:"currency"`
	RemainingMinor *int64      `json:"remaining_minor"`
	TotalMinor     *int64      `json:"total_minor"`
	ExpiresAt      *time.Time  `json:"expires_at"`
	Scope          *UsageScope `json:"scope"`
}

// ResetInventoryDetails reports inventory only, with no redemption handles or
// eligibility semantics. It does not populate RateLimitResetCredits.
type ResetInventoryDetails struct {
	Observation
	ReportedCount *int64        `json:"reported_count"`
	ValidNowCount *int64        `json:"valid_now_count"`
	Windows       []ResetWindow `json:"windows"`
}

type ResetWindow struct {
	Count     int64      `json:"count"`
	ValidFrom *time.Time `json:"valid_from"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type ProductUsageDetails struct {
	Observation
	ParentBucketID *string            `json:"parent_bucket_id"`
	Items          []ProductUsageItem `json:"items"`
}

type ProductUsageItem struct {
	Product         string  `json:"product"`
	ReportedPercent float64 `json:"reported_percent"`
}
