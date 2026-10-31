package grok

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

var errOptionalResponse = errors.New("grok: invalid optional response")

type monetaryConfig struct {
	Used                *minorValue `json:"used"`
	MonthlyLimit        *minorValue `json:"monthlyLimit"`
	OnDemandCap         *minorValue `json:"onDemandCap"`
	CurrentOnDemandUsed *minorValue `json:"currentOnDemandUsed"`
	OnDemandUsed        *minorValue `json:"onDemandUsed"`
	BillingPeriodStart  string      `json:"billingPeriodStart"`
	BillingPeriodEnd    string      `json:"billingPeriodEnd"`
}

func decodeMonetary(body []byte, now time.Time) (*usage.UsageDetails, error) {
	var decoded struct {
		Config              *monetaryConfig `json:"config"`
		OnDemandEnabled     *bool           `json:"onDemandEnabled"`
		CurrentOnDemandUsed *minorValue     `json:"currentOnDemandUsed"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Config == nil {
		return nil, errOptionalResponse
	}
	c := decoded.Config
	item := usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"},
		IncludedUsedMinor: c.Used.minor(), IncludedLimitMinor: c.MonthlyLimit.minor(),
		ExtraCapMinor: c.OnDemandCap.minor(), ExtraUsedMinor: c.CurrentOnDemandUsed.minor(), ExtraEnabled: decoded.OnDemandEnabled}
	if item.ExtraUsedMinor == nil {
		item.ExtraUsedMinor = decoded.CurrentOnDemandUsed.minor()
	}
	if item.ExtraUsedMinor == nil {
		item.ExtraUsedMinor = c.OnDemandUsed.minor()
	}
	if c.BillingPeriodEnd != "" {
		end, err := time.Parse(time.RFC3339Nano, c.BillingPeriodEnd)
		if err != nil {
			return nil, errOptionalResponse
		}
		end = end.UTC()
		item.ResetsAt = &end
	}
	item.StartsAt = billingStart(c.BillingPeriodStart, item.ResetsAt)
	if item.IncludedUsedMinor == nil && item.IncludedLimitMinor == nil && item.ExtraUsedMinor == nil && item.ExtraCapMinor == nil && item.ExtraEnabled == nil {
		return nil, errOptionalResponse
	}
	return &usage.UsageDetails{Finances: &usage.FinancialDetails{Observation: observation(now), Items: []usage.FinancialItem{item}}}, nil
}

func decodeTopUp(body []byte, now time.Time) (*usage.UsageDetails, error) {
	var decoded struct {
		Rule *struct {
			Enabled            *bool       `json:"enabled"`
			MinBeforeHittingSL *minorValue `json:"minBeforeHittingSl"`
			TopUpAmount        *minorValue `json:"topupAmount"`
			MaxAmountPerMonth  *minorValue `json:"maxAmountPerMonth"`
		} `json:"rule"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Rule == nil || decoded.Rule.Enabled == nil {
		return nil, errOptionalResponse
	}
	item := usage.FinancialItem{Currency: "USD", Scope: &usage.UsageScope{Kind: "unknown"}, AutoTopUpEnabled: decoded.Rule.Enabled}
	return &usage.UsageDetails{Finances: &usage.FinancialDetails{Observation: observation(now), Items: []usage.FinancialItem{item}}}, nil
}

func decodeUser(body []byte, now time.Time) (*usage.UsageDetails, error) {
	var decoded struct {
		UserID           string  `json:"userId"`
		PrincipalType    string  `json:"principalType"`
		PrincipalID      string  `json:"principalId"`
		TeamID           *string `json:"teamId"`
		OrgID            *string `json:"organizationId"`
		SubscriptionTier string  `json:"subscriptionTier"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.UserID == "" {
		return nil, errOptionalResponse
	}
	scope := &usage.UsageScope{Kind: "unknown"}
	switch decoded.PrincipalType {
	case "User":
		if decoded.PrincipalID == decoded.UserID && decoded.TeamID == nil && decoded.OrgID == nil {
			scope.Kind = "personal"
		}
	case "Team":
		if decoded.TeamID != nil && decoded.PrincipalID == *decoded.TeamID && decoded.PrincipalID != "" {
			scope.Kind = "team"
		}
	}
	plan := &usage.PlanDetails{Observation: observation(now), Scope: scope}
	if label := safeProduct(decoded.SubscriptionTier); label != "" {
		plan.Label = &label
	}
	return &usage.UsageDetails{Plan: plan}, nil
}
