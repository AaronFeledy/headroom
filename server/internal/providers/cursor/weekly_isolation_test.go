package cursor

import (
	"context"
	"fmt"
	"testing"
	"time"
)

const monthlyMoneySummaryFixture = `{"membershipType":"unified_pro","billingCycleStart":"2026-10-01T00:00:00Z","billingCycleEnd":"2026-11-01T00:00:00Z","individualUsage":{"plan":{"autoPercentUsed":12,"apiPercentUsed":48,"totalPercentUsed":30,"used":5000,"limit":10000,"remaining":5000},"onDemand":{"enabled":true,"used":2500,"limit":10000}}}`

func TestWeeklySelectionExcludesSummaryAllowances(t *testing.T) {
	for _, tt := range []struct {
		name, api, spend, onDemand, start, end string
	}{
		{"zero API", `,"apiPercentUsed":0`, "", `"enabled":true,"used":2500,"limit":10000`, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"absent API", "", "", `"enabled":true,"used":2500,"limit":10000`, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"uncapped summary status", `,"apiPercentUsed":0`, "", `"enabled":true,"used":2500`, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"empty current spend", `,"apiPercentUsed":0`, `,"spendLimitUsage":{}`, `"enabled":true,"used":2500,"limit":10000`, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"matching dates do not prove scope", `,"apiPercentUsed":0`, "", `"enabled":true,"used":2500,"limit":10000`, "2026-10-26T00:00:00Z", "2026-11-02T00:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			summary := fmt.Sprintf(`{"membershipType":"unified_pro","billingCycleStart":%q,"billingCycleEnd":%q,"individualUsage":{"plan":{"apiPercentUsed":48,"used":5000,"limit":10000,"remaining":5000},"onDemand":{%s}}}`, tt.start, tt.end, tt.onDemand)
			period := fmt.Sprintf(`{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"autoPercentUsed":12,"totalPercentUsed":37%s}%s}`, tt.api, tt.spend)
			c := modernClient(t, modernFixture{summary: summary, period: period})
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil {
				t.Fatalf("fetch failed: %v, %v", err, got.Error)
			}
			if len(got.Buckets) != 1 || got.Buckets[0].ID != "auto" || got.Buckets[0].Utilization != 37 || got.Current.Utilization != 37 {
				t.Errorf("weekly quota blended with summary: %#v", got.Buckets)
			}
			if got.UsageDetails.Finances != nil || got.PrimaryStatusText != nil || got.SecondaryStatusText != nil {
				t.Error("summary finance or status imported into weekly result")
			}
			for _, b := range got.Buckets {
				if b.DetailText != nil || b.StatusText != nil {
					t.Errorf("summary money imported into %s", b.ID)
				}
			}
		})
	}
}

func TestPositiveAPIUsesSelectedSourceWindow(t *testing.T) {
	for _, tt := range []struct {
		name, period string
		wantAPI      float64
		start, end   time.Time
	}{
		{"classic", "", 48, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"current weekly", `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"autoPercentUsed":12,"apiPercentUsed":19,"totalPercentUsed":37}}`, 19, time.UnixMilli(1792972800000), time.UnixMilli(1793577600000)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			summary := `{"membershipType":"pro","billingCycleStart":"2026-10-01T00:00:00Z","billingCycleEnd":"2026-11-01T00:00:00Z","individualUsage":{"plan":{"autoPercentUsed":12,"apiPercentUsed":48,"totalPercentUsed":30,"remaining":5000}}}`
			c := modernClient(t, modernFixture{summary: summary, period: tt.period})
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil {
				t.Fatalf("fetch failed: %v, %v", err, got.Error)
			}
			b := bucketByID(got.Buckets, "api")
			if b == nil || b.Utilization != tt.wantAPI || b.Measurement != "percent" || b.StartsAt == nil || b.ResetsAt == nil || !b.StartsAt.Equal(tt.start) || !b.ResetsAt.Equal(tt.end) {
				t.Fatalf("API value or window changed: %#v", b)
			}
			if tt.period == "" {
				finances := got.UsageDetails.Finances
				if finances == nil || len(finances.Items) != 1 || finances.Items[0].IncludedRemainingMinor == nil || *finances.Items[0].IncludedRemainingMinor != 5000 {
					t.Fatal("classic remaining money lost")
				}
			}
		})
	}
}

func TestWeeklyControlsRequireCompatibleScope(t *testing.T) {
	for _, tt := range []struct {
		name, summary, sand string
	}{
		{"summary", `{"membershipType":"unified_pro","onDemandSettings":{"enabled":true,"autoTopUpEnabled":false}}`, ""},
		{"Sand only", monthlyMoneySummaryFixture, `{"usagePercent":15,"hasAvailableUsage":true,"usesPooledEnterpriseAllowance":true,"currentPeriodStart":"2026-10-26T00:00:00Z","nextResetTimestampUtc":"2026-11-02T00:00:00Z","onDemandSettings":{"enabled":true,"autoTopUpEnabled":false}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			c := modernClient(t, modernFixture{summary: tt.summary, period: weeklyPeriodFixture, sand: tt.sand})
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil {
				t.Fatalf("fetch failed: %v, %v", err, got.Error)
			}
			if got.UsageDetails.Finances != nil {
				t.Fatal("unscoped controls exposed as account finances")
			}
			if tt.sand != "" {
				quotas := got.UsageDetails.Quotas
				if len(quotas) != 1 || quotas[0].BucketID != grokBotBucketID || quotas[0].Scope.SharedPool == nil || !*quotas[0].Scope.SharedPool || quotas[0].HasAvailableUsage == nil || !*quotas[0].HasAvailableUsage {
					t.Fatal("Sand-local quota metadata lost")
				}
			}
		})
	}
}
