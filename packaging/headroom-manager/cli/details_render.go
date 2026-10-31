package cli

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func (observation Observation) visible() bool {
	return observation.State == "ok" || observation.State == "stale" && observation.ObservedAt != nil
}

func (observation Observation) context() string {
	if observation.State != "stale" {
		return ""
	}
	return " · stale, observed " + detailDate(observation.ObservedAt)
}

func detailDate(value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return value.Local().Format("2006-01-02 15:04 MST")
}

func scopeText(scope *UsageScope) string {
	if scope == nil {
		return ""
	}
	parts := []string{}
	if scope.Kind == "personal" || scope.Kind == "team" {
		parts = append(parts, scope.Kind)
	}
	if scope.SharedPool != nil {
		if *scope.SharedPool {
			parts = append(parts, "shared pool")
		} else {
			parts = append(parts, "not shared")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, ", ")
}

func boolDetail(label string, value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return label + ": yes"
	}
	return label + ": no"
}

func renderDetails(details *UsageDetails) string {
	if details == nil {
		return ""
	}
	var output strings.Builder
	rows := 0
	write := func(text string) {
		if text == "" || rows >= 64 {
			return
		}
		// Wrap rich rows independently of meter alignment. Every upstream string
		// is sanitized before joining; wrapping never interprets escape sequences.
		line := "  "
		for _, word := range strings.Fields(text) {
			runes := []rune(word)
			for len(runes) > 72 {
				if strings.TrimSpace(line) != "" {
					output.WriteString(strings.TrimRight(line, " ") + "\n")
				}
				output.WriteString("    " + string(runes[:72]) + "\n")
				line, runes = "    ", runes[72:]
			}
			word = string(runes)
			if len([]rune(line))+len([]rune(word))+1 > 78 {
				output.WriteString(strings.TrimRight(line, " ") + "\n")
				line = "    "
			}
			line += word + " "
		}
		output.WriteString(strings.TrimRight(line, " ") + "\n")
		rows++
	}
	if plan := details.Plan; plan != nil && plan.visible() {
		parts := []string{}
		if plan.Label != nil {
			parts = append(parts, terminalText(*plan.Label))
		}
		if value := boolDetail("Unlimited", plan.IsUnlimited); value != "" {
			parts = append(parts, value)
		}
		if len(parts) > 0 || scopeText(plan.Scope) != "" {
			write("Plan: " + strings.Join(parts, " · ") + scopeText(plan.Scope) + plan.context())
		}
	}
	for _, quota := range details.Quotas {
		if !quota.visible() {
			continue
		}
		parts := []string{}
		if quota.PlanLabel != nil {
			parts = append(parts, terminalText(*quota.PlanLabel))
		}
		if quota.HasAvailableUsage != nil {
			label := "Usage available"
			if quota.State == "stale" {
				label = "Usage available at observation"
			}
			parts = append(parts, boolDetail(label, quota.HasAvailableUsage))
		}
		if len(parts) > 0 || scopeText(quota.Scope) != "" {
			write("Quota " + terminalText(quota.BucketID) + ": " + strings.Join(parts, " · ") + scopeText(quota.Scope) + quota.context())
		}
	}
	renderFinancialDetails(details, write)
	if grants := details.CreditGrants; grants != nil && grants.visible() {
		for _, grant := range grants.Items {
			if grant.Currency != "USD" {
				continue
			}
			text := "Grant " + terminalText(grant.DisplayName)
			if grant.Product != nil {
				text += " · product " + terminalText(*grant.Product)
			}
			if grant.RemainingMinor != nil {
				text += " · remaining " + usd(grant.RemainingMinor)
			}
			if grant.TotalMinor != nil {
				text += " · total " + usd(grant.TotalMinor)
			}
			if grant.ExpiresAt != nil {
				text += " · expires " + detailDate(grant.ExpiresAt)
			}
			write(text + scopeText(grant.Scope) + grants.context())
		}
	}
	if inventory := details.ResetInventory; inventory != nil && inventory.visible() {
		context := inventory.context()
		if inventory.ReportedCount != nil {
			write(fmt.Sprintf("Reset inventory: %d reported%s", *inventory.ReportedCount, context))
		}
		if inventory.ValidNowCount != nil {
			label := "in valid windows now"
			if inventory.State == "stale" {
				label = "in valid windows at observation"
			}
			write(fmt.Sprintf("Reset inventory: %d %s%s", *inventory.ValidNowCount, label, context))
		}
		for _, window := range inventory.Windows {
			if window.Count == nil {
				continue
			}
			text := fmt.Sprintf("Reset window: %d reported", *window.Count)
			if window.ValidFrom != nil {
				text += " · valid from " + detailDate(window.ValidFrom)
			}
			if window.ExpiresAt != nil {
				text += " · expires " + detailDate(window.ExpiresAt)
			}
			write(text + context)
		}
	}
	if products := details.ProductUsage; products != nil && products.visible() {
		for _, product := range products.Items {
			percent := product.ReportedPercent
			if percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100 {
				continue
			}
			text := fmt.Sprintf("Product %s: %.1f%% reported contribution", terminalText(product.Product), *percent)
			if products.ParentBucketID != nil {
				text += " · of " + terminalText(*products.ParentBucketID)
			}
			write(text + products.context())
		}
	}
	return output.String()
}

// Suppress a legacy summary only when its corresponding richer facts render.
func hasRichEquivalent(details *UsageDetails, text string) bool {
	if details == nil {
		return false
	}
	clean := terminalText(text)
	return clean != "" && strings.Contains(strings.Join(strings.Fields(renderDetails(details)), " "), clean)
}
