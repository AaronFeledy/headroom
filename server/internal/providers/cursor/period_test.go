package cursor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchSelectsWeeklyPeriodWhenSummaryHasMonthlyZeros(t *testing.T) {
	for _, dates := range []string{
		`"1792972800000","1793577600000"`,
		`1792972800000,1793577600000`,
	} {
		t.Run(dates, func(t *testing.T) {
			// Given: invented monthly placeholders and authoritative weekly usage.
			var start, end string
			for i, v := range strings.Split(dates, ",") {
				if i == 0 {
					start = v
				} else {
					end = v
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case usageSummaryPath:
					fmt.Fprint(w, `{"membershipType":"unified_pro","billingCycleStart":"2026-10-01T00:00:00Z","billingCycleEnd":"2026-11-01T00:00:00Z","individualUsage":{"plan":{"autoPercentUsed":0,"apiPercentUsed":0,"used":0,"limit":0}}}`)
				case "/api/dashboard/get-current-period-usage":
					if r.Method != "POST" || r.Header.Get("Origin") == "" {
						t.Error("missing POST/Origin")
					}
					fmt.Fprintf(w, `{"billingCycleStart":%s,"billingCycleEnd":%s,"planUsage":{"autoPercentUsed":12,"apiPercentUsed":0,"totalPercentUsed":37}}`, start, end)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
			client.discovery.Now = func() time.Time { return time.UnixMilli(1793200000000).UTC() }
			client.SetCookieHeader("synthetic-cookie")
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil {
				t.Fatalf("Fetch failed: %v / %v", err, got.Error)
			}
			b := bucketByID(got.Buckets, "auto")
			if b == nil || b.Utilization != 37 {
				t.Fatalf("weekly bucket = %#v", b)
			}
			if b.StartsAt == nil || b.ResetsAt == nil || b.ResetsAt.Sub(*b.StartsAt) != 7*24*time.Hour {
				t.Fatalf("weekly bounds = %#v", b)
			}
			if bucketByID(got.Buckets, "api") != nil {
				t.Fatal("unified zero API placeholder retained")
			}
		})
	}
}
