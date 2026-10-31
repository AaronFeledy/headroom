package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestSandAvailabilityIsQuotaLocalAndPooledMetersHidden(t *testing.T) {
	for _, tt := range []struct {
		name, body   string
		show         bool
		measurement  string
		availability bool
	}{
		{"pooled", `{"usagePercent":50,"hasNonZeroIncludedLimit":true,"hasAvailableUsage":true,"usesPooledEnterpriseAllowance":true,"grokPlanLabel":"Synthetic Grok"}`, false, "", true},
		{"explicit no allowance zero", `{"usagePercent":0,"hasNonZeroIncludedLimit":false,"hasAvailableUsage":false}`, false, "", false},
		{"positive legacy missing flag", `{"usagePercent":15,"hasAvailableUsage":true}`, true, "percent", true},
		{"real zero", `{"usagePercent":0,"hasNonZeroIncludedLimit":true,"hasAvailableUsage":false}`, true, "percent", false},
		{"status zero", `{"hasNonZeroIncludedLimit":true,"hasAvailableUsage":true}`, true, "status", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			c := modernClient(t, modernFixture{summary: classicSummaryFixture, sand: tt.body})
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil {
				t.Fatal("fetch failed")
			}
			bucket := bucketByID(got.Buckets, grokBotBucketID)
			if (bucket != nil) != tt.show {
				t.Fatalf("Sand visibility=%v", bucket != nil)
			}
			if bucket != nil && bucket.Measurement != tt.measurement {
				t.Fatalf("measurement=%s", bucket.Measurement)
			}
			quotas := got.UsageDetails.Quotas
			if len(quotas) != 1 || quotas[0].BucketID != grokBotBucketID || quotas[0].HasAvailableUsage == nil || *quotas[0].HasAvailableUsage != tt.availability {
				t.Fatal("Sand availability was lost or made global")
			}
		})
	}
}

func TestSandRetryAfterRetainsBoundedStaleQuotaObservation(t *testing.T) {
	for _, mode := range []string{"classic", "weekly"} {
		t.Run(mode, func(t *testing.T) {
			// Given: one observation, a throttled refresh, then a fresh genuine zero.
			var calls atomic.Int32
			c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == currentPeriodPath && mode == "weekly" {
					fmt.Fprint(w, weeklyPeriodFixture)
					return
				}
				if r.URL.Path != sandUsagePath {
					http.NotFound(w, r)
					return
				}
				switch calls.Add(1) {
				case 1:
					fmt.Fprint(w, `{"usagePercent":12,"hasNonZeroIncludedLimit":true,"hasAvailableUsage":true,"grokPlanLabel":"Synthetic Grok","usesPooledEnterpriseAllowance":false}`)
				case 2:
					w.Header().Set("Retry-After", "1200")
					w.WriteHeader(429)
				default:
					fmt.Fprint(w, `{"usagePercent":0,"hasNonZeroIncludedLimit":true,"hasAvailableUsage":true,"grokPlanLabel":"Synthetic Grok","usesPooledEnterpriseAllowance":false}`)
				}
			})
			original := time.UnixMilli(1793200000000).UTC()
			now := original
			c.discovery.Now = func() time.Time { return now }
			for _, stage := range []struct {
				name    string
				minutes int
				calls   int32
				state   string
				percent float64
			}{
				{"initial", 0, 1, "ok", 12},
				{"failed refresh", 6, 2, "stale", 0},
				{"retry deferred", 12, 2, "stale", 0},
				{"expired", 16, 2, "unknown", 0},
				{"fresh again", 27, 3, "ok", 0},
			} {
				t.Run(stage.name, func(t *testing.T) {
					now = original.Add(time.Duration(stage.minutes) * time.Minute)
					// When
					got, err := c.Fetch(context.Background())
					// Then
					if err != nil || got.Error != nil || got.FetchFailure != nil || got.Auth.State != "signed_in" || got.NeedsReauth || calls.Load() != stage.calls {
						t.Fatal("Sand refresh changed core success/auth or ignored Retry-After")
					}
					body, err := json.Marshal(got)
					if err != nil {
						t.Fatal(err)
					}
					var serialized usage.UsageData
					if err := json.Unmarshal(body, &serialized); err != nil {
						t.Fatal(err)
					}
					for _, data := range []usage.UsageData{got, serialized} {
						bucket := bucketByID(data.Buckets, grokBotBucketID)
						if stage.state == "ok" {
							if bucket == nil || bucket.Measurement != "percent" || bucket.Utilization != stage.percent {
								t.Error("fresh Sand percent bucket missing or incorrect")
							}
						} else if bucket != nil {
							t.Error("stale or expired Sand emitted a bucket")
						}
						quotas := data.UsageDetails.Quotas
						if stage.state == "unknown" {
							if quotas != nil {
								t.Error("expired Sand must be unknown, not a zero or known-empty observation")
							}
							continue
						}
						observed := original
						if stage.state == "ok" {
							observed = now
						}
						if len(quotas) != 1 || quotas[0].State != stage.state || quotas[0].ObservedAt == nil || !quotas[0].ObservedAt.Equal(observed) {
							t.Fatal("Sand observation freshness changed")
						}
						q := quotas[0]
						if q.BucketID != grokBotBucketID || q.PlanLabel == nil || *q.PlanLabel != "Synthetic Grok" || q.HasAvailableUsage == nil || !*q.HasAvailableUsage || q.Scope == nil || q.Scope.Kind != "unknown" || q.Scope.SharedPool == nil || *q.Scope.SharedPool {
							t.Error("historical Sand quota metadata lost")
						}
					}
				})
			}
		})
	}
}

func TestOptionalPlanAndSettingsExcludeAccountURLs(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"pro","planName":"Synthetic Pro","onDemandSettings":{"enabled":true,"autoTopUpEnabled":false,"accountUrl":"https://private.example.test/user-secret"},"individualUsage":{"plan":{"totalPercentUsed":0}}}`, plan: `{"planInfo":{"planName":"Synthetic Plan","price":"1900","billingCycleEnd":"1793577600000","includedUsageResetsAt":"1793577600000"}}`})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatal("fetch failed")
	}
	if got.UsageDetails.Plan.Label == nil || *got.UsageDetails.Plan.Label != "Synthetic Plan" {
		t.Fatal("plan label missing")
	}
	if got.UsageDetails.Finances == nil || len(got.UsageDetails.Finances.Items) != 1 || got.UsageDetails.Finances.Items[0].ExtraEnabled == nil || !*got.UsageDetails.Finances.Items[0].ExtraEnabled || got.UsageDetails.Finances.Items[0].AutoTopUpEnabled == nil || *got.UsageDetails.Finances.Items[0].AutoTopUpEnabled {
		t.Fatal("reported settings flags missing")
	}
	if got.UsageDetails.Finances.Items[0].IncludedLimitMinor != nil {
		t.Fatal("plan price invented a quota limit")
	}
}
