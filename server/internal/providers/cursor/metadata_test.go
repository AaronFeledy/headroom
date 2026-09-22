package cursor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBillingMetadataPreservesPoolsAndReportedMoney(t *testing.T) {
	var summary cursorUsageSummary
	body := `{"billingCycleStart":"2026-02-01T00:00:00Z","billingCycleEnd":"2026-03-01T00:00:00Z","isUnlimited":true,"limitType":"team","individualUsage":{"plan":{"used":40000,"limit":40000,"remaining":0,"breakdown":{"included":35000,"bonus":5000,"total":40000},"autoPercentUsed":12,"apiPercentUsed":48}}}`
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatal(err)
	}
	data := baseUsageData()
	populateUsageData(&data, summary, nil, &cursorSandUsage{CurrentPeriodStart: "2026-02-20T00:00:00Z", NextResetTimestampUtc: "2026-02-27T00:00:00Z", HasNonZeroIncludedLimit: true})
	if len(data.Buckets) != 3 {
		t.Fatalf("buckets = %#v", data.Buckets)
	}
	for _, b := range data.Buckets[:2] {
		if b.StartsAt == nil || b.ResetsAt.Sub(*b.StartsAt).Hours() != 28*24 {
			t.Fatalf("wrong period: %#v", b)
		}
		for _, want := range []string{"Plan-wide", "Plan remaining: $0", "Included usage: $350", "Bonus usage: $50", "Total plan usage: $400", "unlimited access", "Limit scope: team"} {
			if b.DetailText == nil || !strings.Contains(*b.DetailText, want) {
				t.Errorf("missing %q", want)
			}
		}
	}
	if data.Buckets[1].Utilization != 48 || *data.Buckets[1].StatusText != "$400 / $400 this cycle" {
		t.Fatalf("reported values changed: %#v", data.Buckets[1])
	}
	if data.Buckets[2].StartsAt == nil || data.Buckets[2].ResetsAt.Sub(*data.Buckets[2].StartsAt).Hours() != 7*24 || data.Buckets[2].DetailText != nil {
		t.Fatal("Grok Bot inherited plan metadata")
	}
}

func TestBillingMetadataMissingAndInvalidRemainOptional(t *testing.T) {
	for _, start := range []string{"", "invalid", "2026-03-01T00:00:00Z", "2027-01-01T00:00:00Z", "2020-01-01T00:00:00Z"} {
		end := "2026-03-01T00:00:00Z"
		data := baseUsageData()
		populateUsageData(&data, cursorUsageSummary{BillingCycleStart: &start, BillingCycleEnd: &end}, nil, nil)
		if data.Buckets[0].StartsAt != nil || data.Buckets[0].DetailText != nil {
			t.Fatalf("invented metadata for %q", start)
		}
	}
	negative := -1
	summary := cursorUsageSummary{IndividualUsage: &cursorIndividualUsage{Plan: &cursorPlanUsage{Remaining: &negative, Breakdown: &cursorPlanBreakdown{Bonus: &negative}}}, LimitType: "<unknown>"}
	if planDetails(summary) != nil {
		t.Fatal("invalid values should not be displayed")
	}
}
