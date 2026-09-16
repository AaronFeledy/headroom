package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_Fetch_returns_usage_when_claude_credentials_are_valid(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "access-token", Refresh: "refresh-token", ExpiresAt: futureMillis(t), Subscription: "max"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/usage" {
			t.Fatalf("path = %q, want /api/oauth/usage", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatalf("anthropic-beta = %q", r.Header.Get("anthropic-beta"))
		}
		if r.Header.Get("User-Agent") != "claude-code/2.1.69" {
			t.Fatalf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		writeResponse(t, w, `{"five_hour":{"utilization":42.5,"resets_at":"2026-07-12T18:30:00-04:00"},"seven_day":{"utilization":71,"resets_at":"2026-07-19T00:00:00Z"}}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL + "/api/oauth/usage", TokenURL: server.URL + "/v1/oauth/token"})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	assertUsageSuccess(t, got, usageExpectation{subtitle: "Max", current: 42.5, weekly: 71})
	if got.Current.ResetsAt == nil || got.Current.ResetsAt.Location() != time.UTC {
		t.Fatalf("current reset not normalized to UTC: %#v", got.Current.ResetsAt)
	}
}

func Test_Client_Fetch_refreshes_expired_token_and_preserves_unknown_fields(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "old-access", Refresh: "old-refresh", ExpiresAt: pastMillis(t), Subscription: "pro", Extra: `,"custom":{"keep":true}`})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			assertRefreshRequest(t, r, "old-refresh")
			writeResponse(t, w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		case "/api/oauth/usage":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeResponse(t, w, `{"five_hour":{"utilization":5},"seven_day":{"utilization":9}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL + "/api/oauth/usage", TokenURL: server.URL + "/v1/oauth/token"})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	assertUsageSuccess(t, got, usageExpectation{subtitle: "Pro", current: 5, weekly: 9})
	updated := readFile(t, credentialsPath)
	for _, want := range []string{`"accessToken":"new-access"`, `"refreshToken":"new-refresh"`, `"subscriptionType":"pro"`, `"custom":{"keep":true}`} {
		if !strings.Contains(compactJSON(t, updated), want) {
			t.Fatalf("updated credentials missing %s in %s", want, updated)
		}
	}
}

func Test_Client_Fetch_returns_reauth_usage_when_refresh_invalid_grant(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "old-access", Refresh: "revoked", ExpiresAt: pastMillis(t)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeResponse(t, w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL + "/api/oauth/usage", TokenURL: server.URL})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !got.NeedsReauth || got.ReauthCommand == nil || *got.ReauthCommand != "claude auth login" {
		t.Fatalf("reauth signaling = needs:%v command:%v", got.NeedsReauth, got.ReauthCommand)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "AUTH_EXPIRED") {
		t.Fatalf("error = %v, want AUTH_EXPIRED", got.Error)
	}
}

func Test_Client_Fetch_reloads_when_credential_mtime_changed_before_refresh(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "stale-access", Refresh: "stale-refresh", ExpiresAt: pastMillis(t)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth/token" {
			t.Fatalf("refresh endpoint must not be called after mtime changed")
		}
		if r.Header.Get("Authorization") != "Bearer fresh-access" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeResponse(t, w, `{"five_hour":{"utilization":12},"seven_day":{"utilization":34}}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL, TokenURL: server.URL + "/v1/oauth/token"})
	if _, err := client.loadCredentials(context.Background()); err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	writeClaudeCredentialsAt(t, credentialsPath, credentialFixture{Access: "fresh-access", Refresh: "fresh-refresh", ExpiresAt: futureMillis(t)})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	assertUsageSuccess(t, got, usageExpectation{current: 12, weekly: 34})
}

func Test_Client_Fetch_forces_refresh_after_401_even_when_token_expiry_is_future(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "revoked-access", Refresh: "refresh-token", ExpiresAt: futureMillis(t)})
	var refreshCalls int32
	var usageCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			atomic.AddInt32(&refreshCalls, 1)
			assertRefreshRequest(t, r, "refresh-token")
			writeResponse(t, w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		case "/api/oauth/usage":
			atomic.AddInt32(&usageCalls, 1)
			if r.Header.Get("Authorization") == "Bearer revoked-access" {
				w.WriteHeader(http.StatusUnauthorized)
				writeResponse(t, w, `{"error":"unauthorized"}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeResponse(t, w, `{"five_hour":{"utilization":22},"seven_day":{"utilization":44}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL + "/api/oauth/usage", TokenURL: server.URL + "/v1/oauth/token"})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	assertUsageSuccess(t, got, usageExpectation{current: 22, weekly: 44})
	if refreshCalls != 1 || usageCalls != 2 {
		t.Fatalf("calls refresh=%d usage=%d, want refresh=1 usage=2", refreshCalls, usageCalls)
	}
}

func Test_Client_Fetch_coalesces_concurrent_forced_401_refreshes(t *testing.T) {
	// Given
	credentialsPath := writeClaudeCredentials(t, credentialFixture{Access: "revoked-access", Refresh: "refresh-token", ExpiresAt: futureMillis(t)})
	var old401s int32
	var refreshCalls int32
	both401s := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			atomic.AddInt32(&refreshCalls, 1)
			assertRefreshRequest(t, r, "refresh-token")
			writeResponse(t, w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		case "/api/oauth/usage":
			if r.Header.Get("Authorization") == "Bearer revoked-access" {
				if atomic.AddInt32(&old401s, 1) == 2 {
					close(both401s)
				}
				<-both401s
				w.WriteHeader(http.StatusUnauthorized)
				writeResponse(t, w, `{"error":"unauthorized"}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeResponse(t, w, `{"five_hour":{"utilization":18},"seven_day":{"utilization":36}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: credentialsPath, HTTPClient: server.Client(), UsageURL: server.URL + "/api/oauth/usage", TokenURL: server.URL + "/v1/oauth/token"})

	// When
	results := make(chan usage.UsageData, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := client.Fetch(context.Background())
			results <- got
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	// Then
	for err := range errs {
		if err != nil {
			t.Fatalf("Fetch returned error: %v", err)
		}
	}
	for got := range results {
		assertUsageSuccess(t, got, usageExpectation{current: 18, weekly: 36})
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
}

type usageExpectation struct {
	subtitle string
	current  float64
	weekly   float64
}

func assertUsageSuccess(t *testing.T, got usage.UsageData, want usageExpectation) {
	t.Helper()
	if got.ProviderName != "Claude" || got.PrimaryLabel != "Current Session" || got.SecondaryLabel != "Weekly" || !got.ShowSecondary {
		t.Fatalf("unexpected labels: %#v", got)
	}
	if got.Current.Utilization != want.current || got.Weekly.Utilization != want.weekly {
		t.Fatalf("utilization = %.1f/%.1f, want %.1f/%.1f", got.Current.Utilization, got.Weekly.Utilization, want.current, want.weekly)
	}
	if want.subtitle != "" && (got.Subtitle == nil || *got.Subtitle != want.subtitle) {
		t.Fatalf("subtitle = %v, want %q", got.Subtitle, want.subtitle)
	}
}
