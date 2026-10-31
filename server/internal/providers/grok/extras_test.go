package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type enrichmentFixture struct {
	provider      *Provider
	now           time.Time
	status        atomic.Int32
	primaryStatus atomic.Int32
	period        atomic.Int32
	calls         [3]atomic.Int32
}

func newEnrichmentFixture(t *testing.T) *enrichmentFixture {
	t.Helper()
	f := &enrichmentFixture{now: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary" {
			if status := f.primaryStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				return
			}
			end := "2026-07-15T00:00:00Z"
			if f.period.Load() != 0 {
				end = "2026-07-22T00:00:00Z"
			}
			_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":40,"prepaidBalance":{"val":1250},"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"` + end + `"},"productUsage":[{"product":"Build","usagePercent":40}]}}`))
			return
		}
		if r.URL.Path == "/settings" {
			_, _ = w.Write([]byte(`{"subscription_tier_display":"SuperGrok"}`))
			return
		}
		index := -1
		switch r.URL.Path {
		case "/ledger":
			index = 0
		case "/topup":
			index = 1
		case "/user":
			index = 2
		}
		if index < 0 {
			http.NotFound(w, r)
			return
		}
		f.calls[index].Add(1)
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-XAI-Token-Auth") != tokenAuthHeader {
			t.Error("missing CLI optional auth")
		}
		if index == 2 && r.URL.Query().Get("include") != "subscription" {
			t.Error("missing subscription query")
		}
		if status := f.status.Load(); status != 0 {
			if status == 999 {
				<-r.Context().Done()
				return
			}
			if status == 429 && index != 1 {
				_, _ = w.Write([]byte(`{"config":{"used":{"val":1}}}`))
				return
			}
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(int(status))
			return
		}
		bodies := []string{`{"config":{"used":{"val":100},"monthlyLimit":{"val":5000},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"},"history":[{"onDemandUsed":{"val":99999}}]}`, `{"rule":{"enabled":true,"maxAmountPerMonth":{"val":3000}}}`, `{"userId":"synthetic-user","principalType":"User","principalId":"synthetic-user","subscriptionTier":"SuperGrok","email":"private@example.test"}`}
		_, _ = w.Write([]byte(bodies[index]))
	}))
	t.Cleanup(server.Close)
	path := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"synthetic-token","user_id":"synthetic-user","expires_at":1800000000000}}`)
	p, err := newIsolatedProvider(Options{CredentialsPath: path, HTTPClient: server.Client(), BillingURL: server.URL + "/primary", SettingsURL: server.URL + "/settings", PlainBillingURL: server.URL + "/ledger", AutoTopUpURL: server.URL + "/topup", UserURL: server.URL + "/user?include=subscription", ResetInventoryURL: server.URL + "/GetRemainingResets", Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.provider = p
	return f
}

func (f *enrichmentFixture) fetch(t *testing.T) usage.UsageData {
	t.Helper()
	data, err := f.provider.Fetch(context.Background())
	if err != nil || data.Error != nil {
		t.Fatalf("core fetch failed: %v %v", err, data.Error)
	}
	return data
}

func TestProviderEnrichmentSurface(t *testing.T) {
	f := newEnrichmentFixture(t)
	data := f.fetch(t)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Buckets) != 1 || data.Buckets[0].Utilization != 40 || len(data.UsageDetails.Finances.Items) != 3 || data.UsageDetails.ProductUsage.ParentBucketID == nil || data.UsageDetails.Plan.Scope.Kind != "personal" || data.RateLimitResetCredits != nil || data.ProviderAccountID != "" {
		t.Fatalf("incorrect surface: %s", encoded)
	}
	for _, secret := range []string{"synthetic-user", "synthetic-token", "private@example.test", "99999"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private or historical data leaked: %s", secret)
		}
	}
	t.Logf("synthetic module surface: %s", encoded)
}

func TestOptionalCacheKeepsObservationAndDetachesReturnedValues(t *testing.T) {
	f := newEnrichmentFixture(t)
	first := f.fetch(t)
	observed := *first.UsageDetails.Plan.ObservedAt
	first.UsageDetails.Plan.Scope.Kind = "team"
	f.now = f.now.Add(time.Minute)
	second := f.fetch(t)
	if f.calls[0].Load() != 1 || f.calls[1].Load() != 1 || f.calls[2].Load() != 1 || !second.UsageDetails.Plan.ObservedAt.Equal(observed) || second.UsageDetails.Plan.Scope.Kind != "personal" {
		t.Fatal("cache cadence, observation or clone violated")
	}
}

func TestOptionalFailuresNeverChangeCoreAuth(t *testing.T) {
	for _, status := range []int32{401, 403, 429, 500} {
		t.Run(http.StatusText(int(status)), func(t *testing.T) {
			f := newEnrichmentFixture(t)
			f.fetch(t)
			f.now = f.now.Add(5 * time.Minute)
			f.status.Store(status)
			data := f.fetch(t)
			if data.NeedsReauth || data.FetchFailure != nil || data.Auth.State != "signed_in" || data.Buckets[0].Utilization != 40 {
				t.Fatal("optional failure poisoned primary")
			}
			if data.UsageDetails.Finances.State != usage.DetailStale || data.UsageDetails.Plan.State != usage.DetailStale {
				t.Fatal("last-good data not stale")
			}
			f.now = f.now.Add(16 * time.Minute)
			data = f.fetch(t)
			if data.UsageDetails.Plan.Scope != nil {
				t.Fatal("expired metadata retained")
			}
			for _, item := range data.UsageDetails.Finances.Items {
				if item.AutoTopUpEnabled != nil || item.IncludedLimitMinor != nil {
					t.Fatal("expired financial metadata retained")
				}
			}
		})
	}
}

func TestOptionalRetryAfterIsPerEndpoint(t *testing.T) {
	f := newEnrichmentFixture(t)
	f.status.Store(429)
	f.fetch(t)
	f.now = f.now.Add(5 * time.Minute)
	f.status.Store(0)
	data := f.fetch(t)
	if f.calls[0].Load() != 2 || f.calls[1].Load() != 1 || f.calls[2].Load() != 2 || len(data.UsageDetails.Finances.Items) != 2 {
		t.Fatal("cooldown was not endpoint-local")
	}
}

func TestOptionalCacheClearsOnCredentialOrPeriodChange(t *testing.T) {
	for _, rotate := range []bool{true, false} {
		f := newEnrichmentFixture(t)
		f.fetch(t)
		if rotate {
			if err := os.WriteFile(f.provider.credentialsPath, []byte(`{"https://auth.x.ai/oauth2/token":{"key":"different-token","user_id":"other-user","expires_at":1800000000000}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(f.provider.credentialsPath, f.now, f.now); err != nil {
				t.Fatal(err)
			}
		} else {
			f.period.Store(1)
		}
		f.status.Store(403)
		data := f.fetch(t)
		if len(data.UsageDetails.Finances.Items) != 1 || data.UsageDetails.Plan.Scope != nil || f.calls[0].Load() != 2 {
			t.Fatal("old context metadata reused")
		}
	}
}

func TestPrimaryFailureDoesNotReadOptionalMetadata(t *testing.T) {
	f := newEnrichmentFixture(t)
	f.primaryStatus.Store(500)
	data, err := f.provider.Fetch(context.Background())
	if err != nil || data.Error == nil || data.UsageDetails != nil || f.calls[0].Load()+f.calls[1].Load()+f.calls[2].Load() != 0 {
		t.Fatal("metadata read after failed primary")
	}
}

func TestOptionalTimeoutPreservesSuccessfulPrimary(t *testing.T) {
	f := newEnrichmentFixture(t)
	f.status.Store(999)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := f.provider.Fetch(ctx)
	if err != nil || data.Error != nil || data.NeedsReauth || len(data.Buckets) != 1 || data.UsageDetails.ResetInventory.State != usage.DetailUnknown {
		t.Fatal("optional timeout failed primary")
	}
}
