package grok

import (
	"fmt"
	"math"
	"strings"
)

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

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func strPtr(value string) *string { return &value }

func billingDetails(config billingConfig) *string {
	lines := []string{}
	if balance := config.PrepaidBalance.minor(); balance != nil {
		lines = append(lines, fmt.Sprintf("Prepaid balance: $%.2f", float64(*balance)/100))
	}
	if config.TopUpMethod == "TOP_UP_METHOD_SAVED_PAYMENT_METHOD" {
		lines = append(lines, "Top-up method: saved payment method")
	}
	if config.IsUnifiedBillingUser != nil && *config.IsUnifiedBillingUser {
		lines = append(lines, "Unified billing reported by Grok")
	}
	seen := map[string]bool{}
	for _, product := range config.ProductUsage {
		name := safeProduct(product.Product)
		percent := product.UsagePercent
		if name == "" || seen[name] || percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100 {
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
