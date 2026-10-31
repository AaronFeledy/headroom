package cli

import (
	"encoding/json"
	"errors"
	"time"
)

// These optional display DTOs mirror the additive read-only contract. They are
// deliberately not used to serialize snapshots: unknown/legacy JSON stays raw.
type UsageDetails struct {
	Plan           *PlanDetails
	Quotas         []QuotaDetails
	Finances       *FinancialDetails
	CreditGrants   *CreditGrantDetails
	ResetInventory *ResetInventoryDetails
	ProductUsage   *ProductUsageDetails
}

type Observation struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
}

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

// Exact nonnegative safe minor units/counts; no float conversion or rounding.
type detailInteger int64

func (value *detailInteger) UnmarshalJSON(data []byte) error {
	var integer int64
	if err := json.Unmarshal(data, &integer); err != nil {
		return err
	}
	if integer < 0 || integer > 9007199254740991 {
		return errors.New("invalid detail integer")
	}
	*value = detailInteger(integer)
	return nil
}

type FinancialDetails struct {
	Observation
	Items []FinancialItem `json:"items"`
}

type FinancialItem struct {
	Currency               string         `json:"currency"`
	Scope                  *UsageScope    `json:"scope"`
	StartsAt               *time.Time     `json:"starts_at"`
	ResetsAt               *time.Time     `json:"resets_at"`
	IncludedUsedMinor      *detailInteger `json:"included_used_minor"`
	IncludedLimitMinor     *detailInteger `json:"included_limit_minor"`
	IncludedRemainingMinor *detailInteger `json:"included_remaining_minor"`
	PrepaidRemainingMinor  *detailInteger `json:"prepaid_remaining_minor"`
	ExtraUsedMinor         *detailInteger `json:"extra_used_minor"`
	ExtraCapMinor          *detailInteger `json:"extra_cap_minor"`
	ExtraEnabled           *bool          `json:"extra_enabled"`
	AutoTopUpEnabled       *bool          `json:"auto_top_up_enabled"`
}

type CreditGrantDetails struct {
	Observation
	Items []CreditGrant `json:"items"`
}

type CreditGrant struct {
	DisplayName    string         `json:"display_name"`
	Product        *string        `json:"product"`
	Currency       string         `json:"currency"`
	RemainingMinor *detailInteger `json:"remaining_minor"`
	TotalMinor     *detailInteger `json:"total_minor"`
	ExpiresAt      *time.Time     `json:"expires_at"`
	Scope          *UsageScope    `json:"scope"`
}

type ResetInventoryDetails struct {
	Observation
	ReportedCount *detailInteger `json:"reported_count"`
	ValidNowCount *detailInteger `json:"valid_now_count"`
	Windows       []ResetWindow  `json:"windows"`
}

type ResetWindow struct {
	Count     *detailInteger `json:"count"`
	ValidFrom *time.Time     `json:"valid_from"`
	ExpiresAt *time.Time     `json:"expires_at"`
}

type ProductUsageDetails struct {
	Observation
	ParentBucketID *string            `json:"parent_bucket_id"`
	Items          []ProductUsageItem `json:"items"`
}

type ProductUsageItem struct {
	Product         string   `json:"product"`
	ReportedPercent *float64 `json:"reported_percent"`
}

func optionalDetail[T any](data []byte) *T {
	var value *T
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return value
}

// A malformed optional section must not reject good meters or sibling sections.
func (details *UsageDetails) UnmarshalJSON(data []byte) error {
	*details = UsageDetails{}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	details.Plan = optionalDetail[PlanDetails](fields["plan"])
	if quotas := optionalDetail[[]QuotaDetails](fields["quotas"]); quotas != nil && len(*quotas) <= 64 {
		details.Quotas = *quotas
	}
	details.Finances = optionalDetail[FinancialDetails](fields["finances"])
	details.CreditGrants = optionalDetail[CreditGrantDetails](fields["credit_grants"])
	details.ResetInventory = optionalDetail[ResetInventoryDetails](fields["reset_inventory"])
	details.ProductUsage = optionalDetail[ProductUsageDetails](fields["product_usage"])
	if details.Finances != nil && len(details.Finances.Items) > 64 {
		details.Finances = nil
	}
	if details.CreditGrants != nil && len(details.CreditGrants.Items) > 64 {
		details.CreditGrants = nil
	}
	if details.ResetInventory != nil && len(details.ResetInventory.Windows) > 64 {
		details.ResetInventory = nil
	}
	if details.ProductUsage != nil && len(details.ProductUsage.Items) > 64 {
		details.ProductUsage = nil
	}
	return nil
}
