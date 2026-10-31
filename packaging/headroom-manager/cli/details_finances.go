package cli

import (
	"fmt"
	"strings"
)

func usd(value *detailInteger) string {
	return fmt.Sprintf("$%d.%02d", *value/100, *value%100)
}

func renderFinancialDetails(details *UsageDetails, write func(string)) {
	finances := details.Finances
	if finances == nil || !finances.visible() {
		return
	}
	for _, item := range finances.Items {
		if item.Currency != "USD" {
			continue
		}
		context := scopeText(item.Scope) + finances.context()
		if item.StartsAt != nil || item.ResetsAt != nil {
			write("Financial period: " + detailDate(item.StartsAt) + " to " + detailDate(item.ResetsAt) + context)
		}
		if item.PrepaidRemainingMinor != nil {
			write("Prepaid balance: " + usd(item.PrepaidRemainingMinor) + context)
		}
		included := []string{}
		if item.IncludedUsedMinor != nil {
			included = append(included, "spent "+usd(item.IncludedUsedMinor))
		}
		if item.IncludedLimitMinor != nil {
			included = append(included, "limit "+usd(item.IncludedLimitMinor))
		}
		if item.IncludedRemainingMinor != nil {
			included = append(included, "remaining "+usd(item.IncludedRemainingMinor))
		}
		if len(included) > 0 {
			write("Included spend: " + strings.Join(included, " · ") + context)
		}
		extra := []string{}
		if item.ExtraUsedMinor != nil {
			extra = append(extra, "used "+usd(item.ExtraUsedMinor))
		}
		if item.ExtraCapMinor != nil {
			extra = append(extra, "cap "+usd(item.ExtraCapMinor))
		}
		if value := boolDetail("Enabled", item.ExtraEnabled); value != "" {
			extra = append(extra, value)
		}
		if len(extra) > 0 {
			write("Extra usage: " + strings.Join(extra, " · ") + context)
		}
		if value := boolDetail("Auto top-up", item.AutoTopUpEnabled); value != "" {
			write(value + context)
		}
	}
}
