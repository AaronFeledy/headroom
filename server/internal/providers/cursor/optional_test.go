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
)

func optionalTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == usageSummaryPath {
			fmt.Fprint(w, classicSummaryFixture)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err := c.SetAccessToken(jwtWithSub("synthetic", "payload")); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOptionalFailuresNeverFailCoreUsage(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			// Given
			c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, `{"malformed":`) })
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil || got.FetchFailure != nil || got.NeedsReauth || got.Auth.State != "signed_in" || len(got.Buckets) != 2 {
				t.Fatalf("optional HTTP %d changed primary result", status)
			}
			if got.UsageDetails.CreditGrants.State != "unknown" {
				t.Fatal("unknown optional data fabricated")
			}
		})
	}
}

func TestOptionalCacheRetainsOriginalObservationAndExpires(t *testing.T) {
	for _, failure := range []string{"status", "empty", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			// Given
			var calls atomic.Int32
			var fail atomic.Bool
			c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case planInfoPath, grantListPath:
					calls.Add(1)
					if fail.Load() {
						switch failure {
						case "status":
							w.WriteHeader(503)
						case "empty":
							fmt.Fprint(w, `{}`)
						case "malformed":
							fmt.Fprint(w, `{"bad":`)
						}
						return
					}
					if r.URL.Path == planInfoPath {
						fmt.Fprint(w, `{"planInfo":{"planName":"Synthetic Pro"}}`)
					} else {
						fmt.Fprint(w, `{"grants":[{"remainingCents":123}]}`)
					}
				default:
					http.NotFound(w, r)
				}
			})
			now := time.UnixMilli(1793200000000).UTC()
			original := now
			c.discovery.Now = func() time.Time { return now }
			if _, err := c.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("five-minute cache not used")
			}
			fail.Store(true)
			now = original.Add(6 * time.Minute)
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil || got.UsageDetails.CreditGrants.State != "stale" || got.UsageDetails.Plan.State != "stale" || !got.UsageDetails.CreditGrants.ObservedAt.Equal(original) {
				t.Fatal("stale metadata lost original observation")
			}
			now = original.Add(16 * time.Minute)
			expired, err := c.Fetch(context.Background())
			if err != nil || expired.Error != nil || expired.UsageDetails.CreditGrants.State != "unknown" || expired.UsageDetails.Plan.Label == nil || *expired.UsageDetails.Plan.Label != "pro" {
				t.Fatal("metadata older than 15 minutes retained")
			}
		})
	}
}

func TestOptionalCacheClearsOnSelectedCredentialOrSourceChange(t *testing.T) {
	// Given
	var calls atomic.Int32
	c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == grantListPath {
			calls.Add(1)
			fmt.Fprint(w, `{"grants":[]}`)
			return
		}
		http.NotFound(w, r)
	})
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.SetAccessToken(jwtWithSub("different-synthetic", "payload")); err != nil {
		t.Fatal(err)
	}
	// When
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.SetDesktopCookie(c.secret.get(), "Fixture browser")
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Then
	if calls.Load() != 3 {
		t.Fatalf("credential/source rotation reused cache: calls=%d", calls.Load())
	}
}

func TestOptionalRetryAfterDoesNotBlockPrimaryFetch(t *testing.T) {
	// Given
	var calls atomic.Int32
	c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == grantListPath || r.URL.Path == grantBalancePath {
			calls.Add(1)
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(429)
			return
		}
		http.NotFound(w, r)
	})
	now := time.UnixMilli(1793200000000).UTC()
	c.discovery.Now = func() time.Time { return now }
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil || calls.Load() != 2 || got.FetchFailure != nil {
		t.Fatal("optional Retry-After blocked primary or retried too early")
	}
}

func TestGrantRPCUsesSelectedJWTAndOnlyFallsBackWhenUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name, list, balance string
		fallback            int32
		knownEmpty          bool
		balanceObserved     bool
	}{
		{"valid", `{"grants":[]}`, `{}`, 0, true, false},
		{"explicit empty", `{"hasCreditGrants":false}`, `{}`, 0, true, false},
		{"unknown fallback balance", `{}`, `{"creditBalanceCents":"1200"}`, 1, false, true},
		{"both unknown", `{}`, `{}`, 1, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var fallback atomic.Int32
			c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == grantListPath || r.URL.Path == grantBalancePath {
					if r.Method != "POST" || r.Header.Get("Connect-Protocol-Version") != "1" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer "+jwtWithSub("synthetic", "payload") || r.Header.Get("Cookie") != "" {
						t.Error("incorrect read-only grant protocol/credential")
					}
					if r.URL.Path == grantBalancePath {
						fallback.Add(1)
						fmt.Fprint(w, tt.balance)
					} else {
						fmt.Fprint(w, tt.list)
					}
					return
				}
				http.NotFound(w, r)
			})
			// When
			got, err := c.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil || fallback.Load() != tt.fallback {
				t.Fatal("unexpected grant fallback")
			}
			grants := got.UsageDetails.CreditGrants
			if (grants.Items != nil) != tt.knownEmpty {
				t.Fatal("unknown grant inventory became exact empty")
			}
			if tt.balanceObserved && (grants.State != "unknown" || grants.ObservedAt == nil) {
				t.Fatal("aggregate observation lost or treated as individual inventory")
			}
			if got.UsageDetails.Finances != nil {
				t.Fatal("grant RPC fabricated financial balance")
			}
		})
	}
}

func TestOptionalTimeoutBudgetIsBounded(t *testing.T) {
	// Given
	c := optionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == planInfoPath || r.URL.Path == sandUsagePath || r.URL.Path == grantListPath || r.URL.Path == grantBalancePath {
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
				return
			}
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	})
	start := time.Now()
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil || got.FetchFailure != nil || got.NeedsReauth {
		t.Fatal("optional timeout failed core")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("optional calls exceeded total budget: %s", elapsed)
	}
}
