package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_BankedMetadata_auxiliary_429_reaches_shared_cooldown(t *testing.T) {
	for _, header := range []string{"600", "0", "Thu, 01 Jan 1970 00:00:00 GMT"} {
		t.Run(header, func(t *testing.T) {
			var usageCalls, profileCalls atomic.Int32
			path := writeClaudeCredentials(t, credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("only read-only GETs permitted")
					w.WriteHeader(405)
					return
				}
				switch r.URL.Path {
				case "/usage":
					if r.URL.RawQuery != "cedar_ember=1" {
						t.Errorf("unexpected usage query: %s", r.URL.RawQuery)
					}
					usageCalls.Add(1)
					writeResponse(t, w, `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"cedar_ember":{"eligible":true,"grants":[]}}`)
				case "/api/oauth/profile":
					profileCalls.Add(1)
					w.Header().Set("Retry-After", header)
					w.WriteHeader(429)
				default:
					t.Error("unexpected request")
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			c := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", APIBaseURL: server.URL, TokenURL: server.URL + "/token", HTTPClient: server.Client()})
			p := poller.New(poller.Options{})
			if err := p.Register(c, true); err != nil {
				t.Fatal(err)
			}
			first, ok, err := p.PollProvider(context.Background(), "Claude")
			if !ok || err != nil || first.Data.Error != nil || first.Data.Auth.State != "signed_in" || len(first.Data.Buckets) == 0 {
				t.Fatal("optional throttling degraded ordinary usage or authentication")
			}
			f := first.Data.FetchFailure
			if f == nil || f.Kind != usage.FailureRateLimited || f.Operation != usage.OperationProfile || f.HTTPStatus != 429 {
				t.Fatalf("missing auxiliary failure: %+v", f)
			}
			d := first.Data.FetchStatus.Diagnostics
			if first.Data.FetchStatus.FailureKind != nil || d == nil || d.CooldownUntil == nil || d.HTTPStatus == nil || *d.HTTPStatus != 429 {
				t.Fatal("missing successful-usage cooldown diagnostics")
			}
			until, err := time.Parse(time.RFC3339Nano, *d.CooldownUntil)
			if err != nil || time.Until(until) < 59*time.Second {
				t.Fatal("auxiliary deadline did not back off")
			}
			if header == "600" && time.Until(until) < 599*time.Second {
				t.Fatal("provider deadline shortened")
			}
			if header == "600" && *d.CooldownSource != "retry_after" || header != "600" && *d.CooldownSource != "backoff" {
				t.Fatal("incorrect cooldown source")
			}
			_, _, _ = p.PollProvider(context.Background(), "Claude")
			if usageCalls.Load() != 1 || profileCalls.Load() != 1 {
				t.Fatal("requests bypassed auxiliary cooldown")
			}
		})
	}
}

func Test_BankedMetadata_failed_profile_lookup_backs_off_and_recovers(t *testing.T) {
	var profiles atomic.Int32
	var healthy atomic.Bool
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("only read-only GETs permitted")
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/usage":
			if r.URL.RawQuery != "cedar_ember=1" {
				t.Errorf("unexpected usage query: %s", r.URL.RawQuery)
			}
			writeResponse(t, w, `{"five_hour":{"utilization":10},"cedar_ember":{"eligible":true,"grants":[]}}`)
		case "/api/oauth/profile":
			profiles.Add(1)
			if !healthy.Load() {
				w.WriteHeader(503)
				return
			}
			writeResponse(t, w, `{"account":{"uuid":"22222222-2222-2222-2222-222222222222"},"organization":{"uuid":"11111111-1111-1111-1111-111111111111"}}`)
		default:
			t.Error("unexpected request")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", APIBaseURL: server.URL, HTTPClient: server.Client()})
	// Credential rotation must discard the prior account's failure cache.
	c.bank = bankCache{token: "old-fixture", profileNext: time.Now().Add(time.Hour), profileDelay: 5 * time.Minute}
	for i := 0; i < 2; i++ {
		d, err := c.Fetch(context.Background())
		if err != nil || d.Error != nil || d.Auth.State != "signed_in" {
			t.Fatal("profile failure changed auth or usage")
		}
	}
	if profiles.Load() != 1 || c.bank.profileDelay != time.Minute {
		t.Fatal("profile failure was retried on every poll or cache survived rotation")
	}
	c.bank.profileNext = time.Now().Add(-time.Second)
	_, _ = c.Fetch(context.Background())
	if profiles.Load() != 2 || c.bank.profileDelay != 2*time.Minute {
		t.Fatal("profile backoff did not increase")
	}
	healthy.Store(true)
	c.bank.profileNext = time.Now().Add(-time.Second)
	d, err := c.Fetch(context.Background())
	if err != nil || d.ProviderAccountID == "" || profiles.Load() != 3 || c.bank.profileDelay != 0 || !c.bank.profileNext.IsZero() {
		t.Fatal("profile recovery did not clear backoff and bind account")
	}
}
