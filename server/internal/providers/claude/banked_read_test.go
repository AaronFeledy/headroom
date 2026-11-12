package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const fixtureUsageWithStatus = `{"five_hour":{"utilization":96},"seven_day":{"utilization":50},"extra_usage":{"is_enabled":true,"monthly_limit":1000,"used_credits":100,"utilization":10},"cedar_ember":{"eligible":true,"next_grant_id":"fixture","grants":[{"id":"fixture","resets_total":4,"resets_left":3,"paused":false,"usable_now":true}]}}`

const fixtureUsageOnly = `{"five_hour":{"utilization":96},"seven_day":{"utilization":50},"extra_usage":{"is_enabled":true,"monthly_limit":1000,"used_credits":100,"utilization":10}}`

func Test_BankedMetadata_one_usage_request_carries_status(t *testing.T) {
	// Given
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture-access", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	var usageCalls, profileCalls atomic.Int32
	var includeStatus atomic.Bool
	includeStatus.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("only read-only GETs are permitted")
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("anthropic-beta") != betaHeader || r.Header.Get("User-Agent") != "claude-cli/"+pinnedCLIVersion+" (external, cli)" {
			t.Error("missing usage headers")
		}
		switch r.URL.Path {
		case "/usage":
			if r.URL.RawQuery != "cedar_ember=1" {
				t.Errorf("unexpected usage query: %s", r.URL.RawQuery)
			}
			usageCalls.Add(1)
			if includeStatus.Load() {
				writeResponse(t, w, fixtureUsageWithStatus)
				return
			}
			writeResponse(t, w, fixtureUsageOnly)
		case "/api/oauth/profile":
			if r.URL.RawQuery != "" {
				t.Errorf("unexpected profile query: %s", r.URL.RawQuery)
			}
			profileCalls.Add(1)
			writeResponse(t, w, `{"account":{"uuid":"22222222-2222-2222-2222-222222222222"},"organization":{"uuid":"11111111-1111-1111-1111-111111111111"}}`)
		default:
			t.Errorf("unexpected read path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, HomeDir: t.TempDir(), HTTPClient: server.Client(), APIBaseURL: server.URL, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token"})
	// When
	first, err := c.Fetch(context.Background())
	if err != nil || first.Error != nil {
		t.Fatalf("fetch failed: %v %v", err, first.Error)
	}
	second, err := c.Fetch(context.Background())
	// Then the status came from the same body, and only one profile lookup ran.
	if err != nil || second.RateLimitResetCredits == nil || second.RateLimitResetCredits.AvailableCount != 3 || second.RateLimitResetCredits.UsableNow == nil || !*second.RateLimitResetCredits.UsableNow {
		t.Fatalf("missing banked metadata: %+v %v", second.RateLimitResetCredits, err)
	}
	if second.ProviderAccountID != "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222" || !matchesAccountFingerprint(second.ProviderAccountID, *second.RateLimitResetCredits.AccountFingerprint) {
		t.Fatal("account binding missing")
	}
	if usageCalls.Load() != 2 || profileCalls.Load() != 1 {
		t.Fatalf("usage GETs=%d profile GETs=%d, want 2 and 1", usageCalls.Load(), profileCalls.Load())
	}
	if !containsBucket(second.Buckets, "extra") {
		t.Fatal("usage body lost extra usage")
	}
	// Given a later response with no parsable status block, retain last-good
	// metadata for at most fifteen minutes.
	includeStatus.Store(false)
	retained, err := c.Fetch(context.Background())
	if err != nil || retained.Error != nil || retained.RateLimitResetCredits == nil || usageCalls.Load() != 3 || profileCalls.Load() != 1 {
		t.Fatal("status-less usage degraded the reading or discarded fresh cached metadata")
	}
	// Given expired last-good metadata.
	c.bankMu.Lock()
	c.bank.goodAt = time.Now().Add(-16 * time.Minute)
	c.bankMu.Unlock()
	expired, err := c.Fetch(context.Background())
	if err != nil || expired.Error != nil || expired.RateLimitResetCredits != nil {
		t.Fatal("expired metadata remained visible or degraded usage")
	}
	if usageCalls.Load() != 4 {
		t.Fatalf("usage GETs=%d, want 4", usageCalls.Load())
	}
}

func Test_BankedMetadata_parameter_rejection_falls_back_once_per_token_window(t *testing.T) {
	// Given a token whose account rejects the cedar_ember parameter.
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture-access", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	var parameterCalls, plainCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/usage" {
			t.Error("only the usage GET is permitted")
			w.WriteHeader(405)
			return
		}
		if r.URL.RawQuery == "cedar_ember=1" {
			parameterCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			writeResponse(t, w, `{"error":{"type":"invalid_request_error"}}`)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		plainCalls.Add(1)
		writeResponse(t, w, fixtureUsageOnly)
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, HTTPClient: server.Client(), UsageURL: server.URL + "/usage"})
	// When the parameterized request is rejected, one plain retry succeeds.
	first, err := c.Fetch(context.Background())
	if err != nil || first.Error != nil || first.RateLimitResetCredits != nil || len(first.Buckets) == 0 {
		t.Fatalf("fallback did not preserve usage: %v %+v", err, first.Error)
	}
	if parameterCalls.Load() != 1 || plainCalls.Load() != 1 {
		t.Fatalf("requests = parameter %d, plain %d; want 1 and 1", parameterCalls.Load(), plainCalls.Load())
	}
	c.emberMu.Lock()
	remaining := time.Until(c.emberUntil)
	c.emberMu.Unlock()
	if remaining < 29*time.Minute || remaining > 30*time.Minute {
		t.Fatalf("rejection memory = %s, want 30m", remaining)
	}
	// Then later polls go straight to the plain URL.
	second, err := c.Fetch(context.Background())
	if err != nil || second.Error != nil || parameterCalls.Load() != 1 || plainCalls.Load() != 2 {
		t.Fatalf("remembered rejection was not honored: parameter=%d plain=%d", parameterCalls.Load(), plainCalls.Load())
	}
	// After the memory window, try the parameter again.
	c.emberMu.Lock()
	c.emberUntil = time.Now().Add(-time.Second)
	c.emberMu.Unlock()
	third, err := c.Fetch(context.Background())
	if err != nil || third.Error != nil || parameterCalls.Load() != 2 || plainCalls.Load() != 3 {
		t.Fatalf("expired rejection did not retry the parameter: parameter=%d plain=%d", parameterCalls.Load(), plainCalls.Load())
	}
}

func Test_BankedMetadata_parameter_rejection_does_not_retry_profile_errors(t *testing.T) {
	// Given a 403 that names the missing profile scope.
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture-access", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RawQuery != "cedar_ember=1" {
			t.Errorf("unexpected fallback query: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"permission_error","message":"missing scope: user:profile"}}`))
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, HTTPClient: server.Client(), UsageURL: server.URL + "/usage"})
	// When
	data, err := c.Fetch(context.Background())
	// Then no plain retry or parameter memory is created.
	if err != nil || data.Error == nil || calls.Load() != 1 || data.FetchFailure == nil || data.FetchFailure.Kind != usage.FailureAuth {
		t.Fatalf("profile-scope 403 was retried or changed: calls=%d failure=%+v", calls.Load(), data.FetchFailure)
	}
}

func Test_BankedMetadata_claude_429_reports_usage_rate_limit_and_upstream_header(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		future bool
	}{
		{"missing", "", false},
		{"zero", "0", false},
		{"elapsed", "Thu, 01 Jan 1970 00:00:00 GMT", false},
		{"future", "600", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeClaudeCredentials(t, credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			c := New(Options{CredentialsPath: path, HomeDir: t.TempDir(), HTTPClient: server.Client(), UsageURL: server.URL + "/usage"})
			// When
			data, err := c.Fetch(context.Background())
			// Then the poller owns the cooldown; the client only reports the reply.
			if err != nil || data.Error == nil || data.FetchFailure == nil || data.FetchFailure.Kind != usage.FailureRateLimited || data.FetchFailure.Operation != usage.OperationUsage || data.FetchFailure.HTTPStatus != http.StatusTooManyRequests {
				t.Fatalf("failure = %+v", data.FetchFailure)
			}
			if got := data.FetchFailure.RetryAfter.After(time.Now()); got != tc.future {
				t.Fatalf("future Retry-After = %t, want %t", got, tc.future)
			}
		})
	}
}
