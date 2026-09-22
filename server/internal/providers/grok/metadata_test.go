package grok

import (
	"strings"
	"testing"
	"time"
)

func TestBillingMetadataUsesCurrentPeriodAndOptionalDetails(t *testing.T) {
	data := baseUsageData()
	body := `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-07-07T04:00:00-05:00","end":"2026-07-14T09:00:00Z"},"creditUsagePercent":40,"prepaidBalance":{"val":1250},"topUpMethod":"TOP_UP_METHOD_SAVED_PAYMENT_METHOD","isUnifiedBillingUser":true,"productUsage":[{"product":"Api","usagePercent":30},{"product":"Web","usagePercent":10},{"product":"Unknown"},{"product":"<img>","usagePercent":2},{"product":"Bad","usagePercent":200}]}}`
	if err := mapBilling(strings.NewReader(body), &data, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := data.Buckets[0]
	if b.StartsAt == nil || b.ResetsAt.Sub(*b.StartsAt).Hours() != 7*24 || b.Utilization != 40 {
		t.Fatalf("bad window: %#v", b)
	}
	for _, want := range []string{"Prepaid balance: $12.50", "Top-up method: saved payment method", "Shared usage pool", "API reported usage: 30.0%", "Web reported usage: 10.0%"} {
		if b.DetailText == nil || !strings.Contains(*b.DetailText, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"Unknown", "<img>", "Bad"} {
		if strings.Contains(*b.DetailText, unwanted) {
			t.Errorf("unexpected %q", unwanted)
		}
	}
	if len(data.Buckets) != 1 {
		t.Fatal("details created additional meters")
	}
}

func TestPrepaidZeroIsDistinctFromMissing(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-07-14T00:00:00Z"},"prepaidBalance":{}}}`, "Prepaid balance: $0.00"},
		{`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-07-14T00:00:00Z"}}}`, ""},
	} {
		data := baseUsageData()
		if err := mapBilling(strings.NewReader(tc.body), &data, time.Now()); err != nil {
			t.Fatal(err)
		}
		b := data.Buckets[0]
		if b.StartsAt != nil {
			t.Fatal("invented start")
		}
		if tc.want == "" {
			if b.DetailText != nil {
				t.Fatal("invented balance")
			}
		} else if b.DetailText == nil || *b.DetailText != tc.want {
			t.Fatal("lost explicit zero")
		}
	}
}

func TestBrowserWeeklyPreservesStart(t *testing.T) {
	start := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	config := &GrokCreditsConfig{CurrentPeriod: &UsagePeriod{Type: UsagePeriodType_USAGE_PERIOD_TYPE_WEEKLY, Start: &Timestamp{Seconds: start.Unix()}, End: &Timestamp{Seconds: end.Unix()}}}
	b := weeklyBucket(config)
	if b.StartsAt == nil || !b.StartsAt.Equal(start) {
		t.Fatal("browser start lost")
	}
	config.CurrentPeriod.Start.Seconds = end.Unix()
	if weeklyBucket(config).StartsAt != nil {
		t.Fatal("accepted empty window")
	}
}
