package codex

import (
	"math"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_PendingGate_when_weeklyUsageIsObserved(t *testing.T) {
	tests := []struct {
		name        string
		utilization float64
		blocked     bool
	}{
		{"recovered below floor", 89.99, false},
		{"unchanged at manual floor", 90, true},
		{"unchanged below former floor", 94.99, true},
		{"at former floor", 95, true},
		{"exhausted", 100, true},
		{"zero is recovery", 0, false},
		{"negative is not recovery", -1, true},
		{"unknown", math.NaN(), true},
		{"positive infinity", math.Inf(1), true},
		{"negative infinity", math.Inf(-1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: seed memory directly, never submit or consume a reset.
			started := time.Unix(1700000000, 0)
			client := &Client{resetAttemptID: "pending-fixture", resetAccountID: "account-fixture", resetStartedAt: started}
			data := usage.UsageData{
				ProviderAccountID: " account-fixture ",
				Buckets:           []usage.Bucket{{ID: usage.BucketWeekly, Utilization: tt.utilization}},
			}
			// When
			client.observeResetUsage(data, started.Add(time.Second))
			// Then: query only the existing read-only gate surface.
			known, blocked := client.ResetAttemptStatus("new-fixture")
			if known || blocked != tt.blocked {
				t.Fatalf("status = (%v, %v), want (false, %v)", known, blocked, tt.blocked)
			}
		})
	}
}

func Test_PendingGate_when_recoveryEvidenceIsChecked(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*usage.UsageData)
		fetchAt time.Duration
		blocked bool
	}{
		{"fresh same account", func(d *usage.UsageData) {}, time.Second, false},
		{"older fetch", func(d *usage.UsageData) {}, -time.Second, true},
		{"equal fetch", func(d *usage.UsageData) {}, 0, true},
		{"foreign account", func(d *usage.UsageData) { d.ProviderAccountID = "other-fixture" }, time.Second, true},
		{"missing account", func(d *usage.UsageData) { d.ProviderAccountID = "" }, time.Second, true},
		{"blank account", func(d *usage.UsageData) { d.ProviderAccountID = " \t " }, time.Second, true},
		{"error", func(d *usage.UsageData) { message := "fixture error"; d.Error = &message }, time.Second, true},
		{"missing weekly", func(d *usage.UsageData) { d.Buckets = nil }, time.Second, true},
		{"unknown window", func(d *usage.UsageData) { d.Buckets[0].ID = "unknown" }, time.Second, true},
		{"session only", func(d *usage.UsageData) { d.Buckets[0].ID = usage.BucketSession }, time.Second, true},
		{"new window alone", func(d *usage.UsageData) { d.Buckets[0].Utilization = 90 }, time.Second, true},
		{"zero credits alone", func(d *usage.UsageData) {
			d.Buckets[0].Utilization = 90
			d.RateLimitResetCredits = &usage.RateLimitResetCredits{AvailableCount: 0}
		}, time.Second, true},
		{"zero credits with recovery", func(d *usage.UsageData) {
			d.RateLimitResetCredits = &usage.RateLimitResetCredits{AvailableCount: 0}
		}, time.Second, false},
		{"missing credits alone", func(d *usage.UsageData) { d.Buckets[0].Utilization = 94.99 }, time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: all readings include a newer window; time alone is not proof.
			started := time.Unix(1700000000, 0)
			newWindow := started.Add(7 * 24 * time.Hour)
			client := &Client{resetAttemptID: "pending-fixture", resetAccountID: "account-fixture", resetStartedAt: started}
			data := usage.UsageData{
				ProviderAccountID: "account-fixture",
				Buckets:           []usage.Bucket{{ID: usage.BucketWeekly, Utilization: 89.99, ResetsAt: &newWindow}},
				Weekly:            usage.UsageBucket{Utilization: 0, ResetsAt: &newWindow},
			}
			tt.change(&data)
			// When
			client.observeResetUsage(data, started.Add(tt.fetchAt))
			// Then
			known, blocked := client.ResetAttemptStatus("new-fixture")
			if known || blocked != tt.blocked {
				t.Fatalf("status = (%v, %v), want (false, %v)", known, blocked, tt.blocked)
			}
		})
	}
}
