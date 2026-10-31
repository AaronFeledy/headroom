package cursor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const weeklyPeriodFixture = `{"billingCycleStart":"1792972800000","billingCycleEnd":1793577600000,"planUsage":{"autoPercentUsed":12,"apiPercentUsed":0,"totalPercentUsed":37}}`
const classicSummaryFixture = `{"membershipType":"pro","individualUsage":{"plan":{"autoPercentUsed":12,"apiPercentUsed":48,"totalPercentUsed":30}}}`

type modernFixture struct {
	summary string
	period  string
	plan    string
	sand    string
	grants  string
	balance string
}

func modernClient(t *testing.T, fixture modernFixture) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		method := http.MethodPost
		switch r.URL.Path {
		case usageSummaryPath:
			method = http.MethodGet
			body = fixture.summary
		case currentPeriodPath:
			body = fixture.period
		case planInfoPath:
			body = fixture.plan
		case sandUsagePath:
			body = fixture.sand
		case grantListPath:
			body = fixture.grants
		case grantBalancePath:
			body = fixture.balance
		case authMePath:
			method = http.MethodGet
		default:
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != method || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	c.discovery.Now = func() time.Time { return time.UnixMilli(1793200000000).UTC() }
	if err := c.SetAccessToken(jwtWithSub("synthetic-user", "payload")); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCurrentPeriodSelectionAuthority(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fixture   modernFixture
		wantError bool
		wantAuto  float64
		wantAPI   bool
		wantPlan  bool
	}{
		{"unified weekly", modernFixture{summary: `{"membershipType":"unified_pro"}`, period: weeklyPeriodFixture}, false, 37, false, false},
		{"unprefixed weekly", modernFixture{summary: `{"membershipType":"premium_future"}`, period: weeklyPeriodFixture}, false, 37, false, false},
		{"unified unsupported cannot use stale zeros", modernFixture{summary: `{"membershipType":"unified_pro","individualUsage":{"plan":{"autoPercentUsed":0,"apiPercentUsed":0}}}`}, true, 0, false, false},
		{"plan enum weekly cannot fallback", modernFixture{summary: classicSummaryFixture, plan: `{"planInfo":{"includedUsagePeriod":2}}`}, true, 0, false, false},
		{"top-level plan weekly cannot fallback", modernFixture{summary: classicSummaryFixture, plan: `{"planInfo":{"planName":"Synthetic Pro"},"includedUsagePeriod":2,"includedUsageResetsAt":"1793577600000"}`}, true, 0, false, false},
		{"plan enum name cannot fallback", modernFixture{summary: classicSummaryFixture, plan: `{"planInfo":{"includedUsagePeriod":"INCLUDED_USAGE_PERIOD_WEEKLY"}}`}, true, 0, false, false},
		{"unknown zeros not classic proof", modernFixture{summary: `{"membershipType":"premium_future","individualUsage":{"plan":{"autoPercentUsed":0,"apiPercentUsed":0}}}`}, true, 0, false, false},
		{"known classic genuine zero", modernFixture{summary: `{"membershipType":"pro","individualUsage":{"plan":{"autoPercentUsed":0,"apiPercentUsed":0}}}`}, false, 0, true, false},
		{"classic split preserved", modernFixture{summary: classicSummaryFixture}, false, 12, true, false},
		{"unknown mixed positive preserved", modernFixture{summary: `{"individualUsage":{"plan":{"autoPercentUsed":12,"apiPercentUsed":48}}}`}, false, 12, true, false},
		{"weekly excludes positive legacy API", modernFixture{summary: classicSummaryFixture, period: weeklyPeriodFixture}, false, 37, false, false},
		{"total only uses plan", modernFixture{summary: `{}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"totalPercentUsed":0}}`}, false, 0, false, true},
		{"missing metrics unknown", modernFixture{summary: `{}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{}}`}, true, 0, false, false},
		{"null metrics unknown", modernFixture{summary: `{}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"autoPercentUsed":null,"apiPercentUsed":null,"totalPercentUsed":null}}`}, true, 0, false, false},
		{"unified missing dates cannot fallback", modernFixture{summary: `{"membershipType":"unified_pro"}`, period: `{"planUsage":{"totalPercentUsed":20}}`}, true, 0, false, false},
		{"unified stale dates cannot fallback", modernFixture{summary: `{"membershipType":"unified_pro"}`, period: `{"billingCycleStart":1692972800000,"billingCycleEnd":1693577600000,"planUsage":{"totalPercentUsed":20}}`}, true, 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			c := modernClient(t, tt.fixture)
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || (got.Error != nil) != tt.wantError {
				t.Fatalf("error=%v provider error=%v", err, got.Error)
			}
			if tt.wantError {
				if len(got.Buckets) != 0 || got.NeedsReauth {
					t.Fatal("unknown usage exposed quota/signout")
				}
				return
			}
			if (bucketByID(got.Buckets, "api") != nil) != tt.wantAPI || (bucketByID(got.Buckets, "plan") != nil) != tt.wantPlan {
				t.Fatalf("unexpected bucket IDs: %#v", got.Buckets)
			}
			if !tt.wantPlan {
				if b := bucketByID(got.Buckets, "auto"); b == nil || b.Utilization != tt.wantAuto {
					t.Fatalf("auto=%#v", b)
				}
			}
			for _, b := range got.Buckets {
				if b.Measurement != "percent" {
					t.Fatalf("missing measurement: %#v", b)
				}
			}
		})
	}
}

func TestWeeklyMappingDoesNotInheritMonthlyFinances(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"unified_pro","individualUsage":{"plan":{"used":5000,"limit":10000,"remaining":5000}}}`, period: weeklyPeriodFixture})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatal("fetch failed")
	}
	if got.UsageDetails.Finances != nil || got.PrimaryStatusText != nil {
		t.Fatal("monthly dollars inherited by weekly quota")
	}
	for _, b := range got.Buckets {
		if b.DetailText != nil || b.StatusText != nil {
			t.Fatal("fabricated weekly money")
		}
	}
}

func TestLegacyStatusOnlyZeroDiffersFromMeasuredZero(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"pro","individualUsage":{"plan":{"totalPercentUsed":0},"onDemand":{"enabled":true,"used":0}}}`})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatal("fetch failed")
	}
	plan, onDemand := bucketByID(got.Buckets, "plan"), bucketByID(got.Buckets, "on_demand")
	if plan == nil || onDemand == nil || plan.Measurement != "percent" || onDemand.Measurement != "status" || plan.Utilization != 0 || onDemand.Utilization != 0 {
		t.Fatalf("buckets=%#v", got.Buckets)
	}
}
