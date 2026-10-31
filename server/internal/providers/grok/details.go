package grok

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// Cent values in monetary responses are USD minor units. A present empty
// protobuf scalar wrapper is zero; a missing wrapper is unknown.
type minorValue struct {
	Value json.Number `json:"val"`
}

func (v *minorValue) minor() *int64 {
	if v == nil {
		return nil
	}
	raw := string(v.Value)
	if raw == "" {
		raw = "0"
	}
	if len(raw) > 64 {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || value > usage.MaxSafeInteger {
		return nil
	}
	return &value
}

func (v *minorValue) UnmarshalJSON(body []byte) error {
	v.Value = "invalid"
	var decoded struct {
		Value json.RawMessage `json:"val"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	if string(body) == "null" {
		return nil
	}
	if len(decoded.Value) == 0 {
		v.Value = "0"
		return nil
	}
	if string(decoded.Value) == "null" {
		v.Value = "invalid"
		return nil
	}
	if err := json.Unmarshal(decoded.Value, &v.Value); err != nil {
		v.Value = "invalid"
	}
	return nil
}

func observation(now time.Time) usage.Observation {
	now = now.UTC()
	return usage.Observation{State: usage.DetailOK, ObservedAt: &now}
}

func (v *billingValue) UnmarshalJSON(body []byte) error {
	*v = billingValue{}
	var decoded struct {
		Value *json.Number `json:"val"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	var raw minorValue
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	v.raw = &raw
	if string(body) == "null" {
		v.raw = nil
	}
	if decoded.Value == nil || len(*decoded.Value) > 64 {
		return nil
	}
	value, err := decoded.Value.Float64()
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > float64(usage.MaxSafeInteger) {
		return nil
	}
	v.Value = &value
	return nil
}

func safeProduct(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > 40 {
		return ""
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == ' ' || ch == '_' || ch == '-') {
			return ""
		}
	}
	return name
}

func attachBillingDetails(decoded billingResponse, data *usage.UsageData, now time.Time) {
	config := decoded.Config
	details := &usage.UsageDetails{}
	if balance := config.PrepaidBalance.minor(); balance != nil {
		details.Finances = &usage.FinancialDetails{Observation: observation(now), Items: []usage.FinancialItem{{
			Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"}, PrepaidRemainingMinor: balance,
		}}}
	}
	if used, cap := config.OnDemandUsed.raw.minor(), config.OnDemandCap.raw.minor(); used != nil || cap != nil || decoded.OnDemandEnabled != nil {
		if details.Finances == nil {
			details.Finances = &usage.FinancialDetails{Observation: observation(now), Items: []usage.FinancialItem{}}
		}
		details.Finances.Items = append(details.Finances.Items, usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"}, ExtraUsedMinor: used, ExtraCapMinor: cap, ExtraEnabled: decoded.OnDemandEnabled})
	}
	if config.ProductUsage != nil && len(config.ProductUsage) <= usage.MaxDetailItems {
		products := &usage.ProductUsageDetails{Observation: observation(now), Items: []usage.ProductUsageItem{}}
		for _, bucket := range data.Buckets {
			if bucket.ID == usage.BucketWeekly {
				products.ParentBucketID = strPtr(usage.BucketWeekly)
			}
		}
		seen := map[string]bool{}
		for _, item := range config.ProductUsage {
			name := safeProduct(item.Product)
			// Preserve the existing JSON contract: omitted percentages are unknown.
			percent := item.UsagePercent
			if name == "" || seen[name] || percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100 {
				continue
			}
			seen[name] = true
			products.Items = append(products.Items, usage.ProductUsageItem{Product: name, ReportedPercent: *percent})
		}
		details.ProductUsage = products
	}
	data.UsageDetails = details
}
