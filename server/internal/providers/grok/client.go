package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type billingResponse struct {
	Config          billingConfig `json:"config"`
	OnDemandEnabled *bool         `json:"onDemandEnabled"`
}

type billingConfig struct {
	BillingPeriodStart   string                `json:"billingPeriodStart"`
	PrepaidBalance       *billingValue         `json:"prepaidBalance"`
	TopUpMethod          string                `json:"topUpMethod"`
	IsUnifiedBillingUser *bool                 `json:"isUnifiedBillingUser"`
	ProductUsage         []billingProductUsage `json:"productUsage"`
	Used                 billingValue          `json:"used"`
	MonthlyLimit         billingValue          `json:"monthlyLimit"`
	OnDemandUsed         billingValue          `json:"onDemandUsed"`
	OnDemandCap          billingValue          `json:"onDemandCap"`
	BillingPeriodEnd     string                `json:"billingPeriodEnd"`
	CreditUsagePercent   *float64              `json:"creditUsagePercent"`
	CurrentPeriod        *billingPeriod        `json:"currentPeriod"`
}

type billingValue struct {
	Value *float64 `json:"val"`
}

type billingPeriodType string

const billingPeriodTypeWeekly billingPeriodType = "USAGE_PERIOD_TYPE_WEEKLY"

type billingPeriod struct {
	Start string            `json:"start"`
	Type  billingPeriodType `json:"type"`
	End   string            `json:"end"`
}

type settingsResponse struct {
	SubscriptionTierDisplay string `json:"subscription_tier_display"`
}

func (p *Provider) sendGet(ctx context.Context, requestURL string, creds credentials) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Grok request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("X-XAI-Token-Auth", tokenAuthHeader)
	req.Header.Set("x-userid", creds.userID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Grok request: %w", err)
	}
	return resp, nil
}

func mapBilling(body io.Reader, data *usage.UsageData, now time.Time) error {
	var decoded billingResponse
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode billing: %w", err)
	}
	buckets := make([]usage.Bucket, 0, 2)
	var weekly *usage.Bucket
	period := decoded.Config.CurrentPeriod
	if period != nil && period.Type == billingPeriodTypeWeekly {
		reset, err := time.Parse(time.RFC3339, period.End)
		if err == nil {
			reset = reset.UTC()
			utilization := 0.0
			if decoded.Config.CreditUsagePercent != nil {
				utilization = *decoded.Config.CreditUsagePercent
			}
			weekly = &usage.Bucket{
				ID:          usage.BucketWeekly,
				Label:       "Weekly",
				Utilization: math.Max(0, math.Min(100, utilization)),
				ResetsAt:    &reset,
				StartsAt:    billingStart(period.Start, &reset),
			}
		}
	}
	used := decoded.Config.Used.Value
	limit := decoded.Config.MonthlyLimit.Value
	onDemandUsed := decoded.Config.OnDemandUsed.Value
	onDemandCap := decoded.Config.OnDemandCap.Value
	legacyMapped := used != nil && limit != nil && *limit > 0 && decoded.Config.BillingPeriodEnd != ""
	var legacyReset time.Time
	if legacyMapped {
		reset, err := time.Parse(time.RFC3339, decoded.Config.BillingPeriodEnd)
		if err != nil {
			if weekly == nil {
				return fmt.Errorf("parse billing period end: %w", err)
			}
			legacyMapped = false
		} else {
			legacyReset = reset.UTC()
			percent := math.Max(0, math.Min(100, *used / *limit * 100))
			primaryStatus := fmt.Sprintf("%s / %s credits · %s", formatWholeNumber(*used), formatWholeNumber(*limit), formatResetDate(legacyReset, now))
			buckets = append(buckets, usage.Bucket{ID: usage.BucketCredits, Label: "Credits", Utilization: percent, ResetsAt: &legacyReset, StartsAt: billingStart(decoded.Config.BillingPeriodStart, &legacyReset), StatusText: &primaryStatus})
			data.PrimaryStatusText = &primaryStatus
		}
	}
	var disabledStatus *string
	if onDemandBucket, ok := onDemandMeter(decoded.OnDemandEnabled, onDemandUsed, onDemandCap); ok {
		buckets = append(buckets, onDemandBucket)
	} else if legacyMapped {
		disabledStatus = strPtr("Pay as you go disabled")
	}
	if weekly != nil {
		buckets = append(buckets, *weekly)
	}
	if len(buckets) == 0 {
		return errorsChangedResponse()
	}
	secondaryLabel := data.SecondaryLabel
	showSecondary := data.ShowSecondary
	legacyOnly := legacyMapped && weekly == nil
	details := billingDetails(decoded.Config)
	for i := range buckets {
		buckets[i].DetailText = details
	}
	*data = data.WithBuckets(buckets)
	switch {
	case weekly != nil:
		data.SecondaryStatusText = nil
	case len(data.Buckets) >= 2:
		data.SecondaryStatusText = data.Buckets[1].StatusText
	default:
		data.SecondaryStatusText = disabledStatus
	}
	if legacyOnly {
		data.Weekly = usage.UsageBucket{Utilization: 0, ResetsAt: &legacyReset}
		data.SecondaryLabel = secondaryLabel
		data.ShowSecondary = showSecondary
	}
	return nil
}

func (p *Provider) populateSettings(ctx context.Context, creds credentials, data *usage.UsageData) {
	resp, err := p.sendGet(ctx, p.settingsURL, creds)
	if err != nil {
		return
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if err := drainAndClose(resp); err != nil {
			return
		}
		return
	}
	var decoded settingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		if closeErr := drainAndClose(resp); closeErr != nil {
			return
		}
		return
	}
	if err := drainAndClose(resp); err != nil {
		return
	}
	if decoded.SubscriptionTierDisplay != "" {
		data.Subtitle = strPtr(decoded.SubscriptionTierDisplay)
	}
}

func formatResetDate(reset time.Time, now time.Time) string {
	localReset := reset.Local()
	format := "Jan 2"
	if localReset.Year() != now.Local().Year() {
		format = "Jan 2, 2006"
	}
	return "Resets " + localReset.Format(format)
}

func drainAndClose(resp *http.Response) error {
	_, copyErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(wrapIfErr("drain Grok response body", copyErr), wrapIfErr("close Grok response body", closeErr))
	}
	return nil
}

func wrapIfErr(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func formatWholeNumber(value float64) string {
	raw := fmt.Sprintf("%.0f", value)
	start := 0
	if raw[0] == '-' {
		start = 1
	}
	for i := len(raw) - 3; i > start; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	return raw
}

// onDemandMeter builds the pay-as-you-go meter. enabled is nil for legacy
// responses that omit onDemandEnabled (a positive cap then implies enabled);
// used/capacity are nil when their scalar was omitted. Utilization is set only
// when used and a positive cap support a truthful percentage, else the meter is
// status-only (zero utilization) so the tray never paints a misleading 0% bar.
func onDemandMeter(enabled *bool, used *float64, capacity *float64) (usage.Bucket, bool) {
	usedVal := 0.0
	if used != nil {
		usedVal = *used
	}
	capVal := 0.0
	if capacity != nil {
		capVal = *capacity
	}
	var included bool
	if enabled != nil {
		included = *enabled || usedVal > 0
	} else {
		included = usedVal > 0 || capVal > 0
	}
	if !included {
		return usage.Bucket{}, false
	}
	status := onDemandStatusText(used, usedVal, capVal)
	bucket := usage.Bucket{ID: usage.BucketOnDemand, Label: "Pay as you go", StatusText: &status}
	if used != nil && capVal > 0 {
		bucket.Utilization = math.Max(0, math.Min(100, usedVal/capVal*100))
	}
	return bucket, true
}

func onDemandStatusText(used *float64, usedVal float64, capVal float64) string {
	switch {
	case used != nil && capVal > 0:
		return fmt.Sprintf("%s / %s pay-as-you-go", formatWholeNumber(usedVal), formatWholeNumber(capVal))
	case capVal > 0:
		return fmt.Sprintf("%s pay-as-you-go cap", formatWholeNumber(capVal))
	case usedVal > 0:
		return fmt.Sprintf("%s pay-as-you-go used", formatWholeNumber(usedVal))
	default:
		return "Pay as you go enabled"
	}
}

func errorsChangedResponse() error {
	return fmt.Errorf("Grok billing response changed")
}

func billingStart(value string, end *time.Time) *time.Time {
	start, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return usage.ValidPeriodStart(&start, end)
}

type billingProductUsage struct {
	Product      string   `json:"product"`
	UsagePercent *float64 `json:"usagePercent"`
}

func billingDetails(config billingConfig) *string {
	lines := []string{}
	if config.PrepaidBalance != nil {
		balance := 0.0 // An empty Cent object represents zero in protobuf JSON.
		if config.PrepaidBalance.Value != nil {
			balance = *config.PrepaidBalance.Value
		}
		if !math.IsNaN(balance) && !math.IsInf(balance, 0) && balance >= 0 {
			lines = append(lines, fmt.Sprintf("Prepaid balance: $%.2f", balance/100))
		}
	}
	if config.TopUpMethod == "TOP_UP_METHOD_SAVED_PAYMENT_METHOD" {
		lines = append(lines, "Top-up method: saved payment method")
	}
	if config.IsUnifiedBillingUser != nil && *config.IsUnifiedBillingUser {
		lines = append(lines, "Shared usage pool across Grok products")
	}
	seen := map[string]bool{}
	for _, product := range config.ProductUsage {
		name := strings.TrimSpace(product.Product)
		// Product identifiers must remain short plain labels in a tooltip.
		validName := name != "" && len(name) <= 40
		for _, ch := range name {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == ' ' || ch == '_' || ch == '-') {
				validName = false
			}
		}
		percent := product.UsagePercent
		if !validName || seen[name] || percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100 {
			continue
		}
		if len(seen) == 12 {
			break
		}
		seen[name] = true
		if name == "Api" {
			name = "API"
		}
		lines = append(lines, fmt.Sprintf("%s reported usage: %.1f%%", name, *percent))
	}
	if len(lines) == 0 {
		return nil
	}
	detail := strings.Join(lines, "\n")
	return &detail
}
