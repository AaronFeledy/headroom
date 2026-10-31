package cursor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestConfirmedWeeklyNeverRevertsToSummaryAfterPeriodFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		kind   usage.FailureKind
		status int
	}{
		{"rate limited", usage.FailureRateLimited, http.StatusTooManyRequests},
		{"server failure", usage.FailureTransient, http.StatusInternalServerError},
		{"timeout", usage.FailureTransient, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var unavailable atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.RequestURI() {
				case "GET " + usageSummaryPath:
					fmt.Fprint(w, classicSummaryFixture)
				case "POST " + currentPeriodPath:
					if unavailable.Load() {
						switch tt.status {
						case 0:
							if _, err := io.Copy(io.Discard, r.Body); err != nil {
								t.Errorf("read period request: %v", err)
								return
							}
							<-r.Context().Done()
						case http.StatusTooManyRequests:
							w.Header().Set("Retry-After", "120")
							w.WriteHeader(tt.status)
						default:
							w.WriteHeader(tt.status)
						}
						return
					}
					fmt.Fprint(w, weeklyPeriodFixture)
				case "POST " + planInfoPath, "POST " + sandUsagePath, "POST " + grantListPath, "POST " + grantBalancePath:
					http.NotFound(w, r)
				default:
					t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			c := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
			c.discovery.Now = func() time.Time { return time.UnixMilli(1793200000000).UTC() }
			c.SetCookieHeader("fixture-cookie")
			first, err := c.Fetch(context.Background())
			if err != nil || first.Error != nil {
				t.Fatal("weekly confirmation failed")
			}
			unavailable.Store(true)
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error == nil || len(got.Buckets) != 0 || got.UsageDetails != nil || got.NeedsReauth || got.FetchFailure == nil || got.FetchFailure.Kind != tt.kind {
				t.Fatal("confirmed weekly fell back or lost primary failure classification")
			}
			if tt.status == http.StatusTooManyRequests && got.FetchFailure.RetryAfter.IsZero() {
				t.Fatal("weekly failure lost Retry-After")
			}
		})
	}
}

func TestWeeklyExplicitZeroKeepsPercentMeasurement(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"unified_pro"}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"autoPercentUsed":0,"apiPercentUsed":0,"totalPercentUsed":0}}`})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil || len(got.Buckets) != 1 || got.Buckets[0].ID != "auto" || got.Buckets[0].Utilization != 0 || got.Buckets[0].Measurement != "percent" || got.Current.Utilization != 0 {
		t.Fatal("explicit weekly zero was treated as absent")
	}
}

func TestNewSpendLimitUsesMatchingPoolAndBounds(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"unified_pro","billingCycleEnd":"2026-11-01T00:00:00Z","individualUsage":{"onDemand":{"used":100,"limit":100}}}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"totalPercentUsed":37},"spendLimitUsage":{"individualUsed":42,"individualLimit":100,"pooledUsed":"125","pooledLimit":"500","pooledRemaining":"375","limitType":"team"}}`})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatal("fetch failed")
	}
	b := bucketByID(got.Buckets, "on_demand")
	if b == nil || b.Utilization != 25 || b.StartsAt == nil || b.ResetsAt == nil || b.ResetsAt.Sub(*b.StartsAt) != 7*24*time.Hour || b.Measurement != "percent" {
		t.Fatalf("on-demand=%#v", b)
	}
}

func TestFinancialFormattingPreservesSafeIntegerCents(t *testing.T) {
	// Given / When
	got := exactDollars(9007199254740991)
	// Then
	if got != "90071992547409.91" {
		t.Fatalf("money rounded: %s", got)
	}
}
