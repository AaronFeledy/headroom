package api

import (
	"math"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_CodexManualEligibility_when_weeklyUsageIsChecked(t *testing.T) {
	tests := []struct {
		name        string
		utilization float64
		want        bool
	}{
		{"below floor", 89.99, false},
		{"at floor", 90, true},
		{"below former floor", 94.99, true},
		{"at former floor", 95, true},
		{"exhausted", 100, true},
		{"zero", 0, false},
		{"negative", -1, false},
		{"unknown", math.NaN(), false},
		{"positive infinity", math.Inf(1), false},
		{"negative infinity", math.Inf(-1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: synthetic usage only; no handler or provider invocation.
			entry := poller.Entry{Data: usage.UsageData{
				ProviderAccountID:     " account-fixture ",
				RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 1},
				Buckets:               []usage.Bucket{{ID: usage.BucketWeekly, Utilization: tt.utilization}},
			}}
			// When
			got := eligibleForCodexReset(entry)
			// Then
			if got != tt.want {
				t.Fatalf("eligible = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_CodexManualEligibility_when_requiredEvidenceIsAbsent(t *testing.T) {
	tests := []struct {
		name   string
		change func(*usage.UsageData)
	}{
		{"error", func(d *usage.UsageData) { message := "fixture error"; d.Error = &message }},
		{"missing account", func(d *usage.UsageData) { d.ProviderAccountID = "" }},
		{"blank account", func(d *usage.UsageData) { d.ProviderAccountID = " \t " }},
		{"missing credits", func(d *usage.UsageData) { d.RateLimitResetCredits = nil }},
		{"zero credits", func(d *usage.UsageData) { d.RateLimitResetCredits.AvailableCount = 0 }},
		{"negative credits", func(d *usage.UsageData) { d.RateLimitResetCredits.AvailableCount = -1 }},
		{"missing weekly", func(d *usage.UsageData) { d.Buckets = nil }},
		{"session only", func(d *usage.UsageData) { d.Buckets[0].ID = usage.BucketSession }},
		{"unknown window", func(d *usage.UsageData) { d.Buckets[0].ID = "unknown" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			entry := poller.Entry{Data: usage.UsageData{
				ProviderAccountID:     "account-fixture",
				RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 1},
				Buckets:               []usage.Bucket{{ID: usage.BucketWeekly, Utilization: 100}},
				Weekly:                usage.UsageBucket{Utilization: 100},
			}}
			tt.change(&entry.Data)
			// When
			got := eligibleForCodexReset(entry)
			// Then
			if got {
				t.Fatal("incomplete evidence must not permit a manual reset")
			}
		})
	}
}
