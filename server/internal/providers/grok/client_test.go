package grok

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func Test_Provider_Fetch_maps_billing_and_settings_response_to_usage_data(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	server := newGrokTestServer(t, grokTestHooks{})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Subtitle == nil || *data.Subtitle != "SuperGrok" {
		t.Fatalf("Subtitle = %v, want SuperGrok", data.Subtitle)
	}
	if data.PrimaryStatusText == nil || !strings.Contains(*data.PrimaryStatusText, "25 / 100 credits") {
		t.Fatalf("PrimaryStatusText = %v, want credits status", data.PrimaryStatusText)
	}
	if data.SecondaryStatusText == nil || *data.SecondaryStatusText != "40 pay-as-you-go cap" {
		t.Fatalf("SecondaryStatusText = %v, want pay-as-you-go cap", data.SecondaryStatusText)
	}
	if data.Current.ResetsAt == nil || data.Current.ResetsAt.UTC().Format(time.RFC3339) != "2026-08-01T00:00:00Z" {
		t.Fatalf("Current.ResetsAt = %v, want billing period end", data.Current.ResetsAt)
	}
	if data.Weekly.Utilization != 0 || data.Weekly.ResetsAt == nil || !data.Weekly.ResetsAt.Equal(*data.Current.ResetsAt) {
		t.Fatalf("Weekly = %#v, want zero placeholder with same reset", data.Weekly)
	}
	if len(data.Buckets) != 2 || data.Buckets[0].ID != "credits" || data.Buckets[0].Label != "Credits" || data.Buckets[0].Utilization != data.Current.Utilization {
		t.Fatalf("Buckets = %#v, want Credits and on-demand buckets", data.Buckets)
	}
	if data.Buckets[0].StatusText == nil || !strings.Contains(*data.Buckets[0].StatusText, "25 / 100 credits") {
		t.Fatalf("Credits StatusText = %v, want credit totals", data.Buckets[0].StatusText)
	}
	if data.Buckets[1].ID != "on_demand" || data.Buckets[1].StatusText == nil || *data.Buckets[1].StatusText != "40 pay-as-you-go cap" {
		t.Fatalf("On-demand bucket = %#v, want enabled cap status", data.Buckets[1])
	}
	assertGrokHeaders(t, server.billingHeaders[0])
}

func Test_formatResetDate_includes_year_when_local_reset_year_differs_from_current_year(t *testing.T) {
	// Given
	now := time.Date(2026, time.December, 31, 12, 0, 0, 0, time.Local)
	reset := time.Date(2027, time.January, 1, 1, 0, 0, 0, time.Local)

	// When
	got := formatResetDate(reset, now)

	// Then
	if got != "Resets Jan 1, 2027" {
		t.Fatalf("formatResetDate = %q, want Resets Jan 1, 2027", got)
	}
}

func Test_formatResetDate_omits_year_when_local_reset_year_matches_current_year(t *testing.T) {
	// Given
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.Local)
	reset := time.Date(2026, time.August, 1, 1, 0, 0, 0, time.Local)

	// When
	got := formatResetDate(reset, now)

	// Then
	if got != "Resets Aug 1" {
		t.Fatalf("formatResetDate = %q, want Resets Aug 1", got)
	}
}

func Test_Provider_Fetch_returns_billing_error_when_billing_request_fails(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error == nil || !strings.Contains(*data.Error, "502") {
		t.Fatalf("UsageData.Error = %v, want billing status", data.Error)
	}
}

func Test_Provider_Fetch_ignores_settings_failure_after_billing_succeeds(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/settings" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
}

func Test_Provider_Fetch_returns_context_error_when_request_is_canceled(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL, SettingsURL: server.URL, TokenURL: server.URL})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, err = provider.Fetch(ctx)

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch error = %v, want context.Canceled", err)
	}
}

func assertGrokHeaders(t *testing.T, got http.Header) {
	t.Helper()
	if got.Get("Authorization") != "Bearer token" {
		t.Fatalf("Authorization = %q", got.Get("Authorization"))
	}
	if got.Get("X-Xai-Token-Auth") != "xai-grok-cli" {
		t.Fatalf("X-XAI-Token-Auth = %q", got.Get("X-Xai-Token-Auth"))
	}
	if got.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q", got.Get("Accept"))
	}
	if got.Get("User-Agent") != "ClaudeUsageWidget" {
		t.Fatalf("User-Agent = %q", got.Get("User-Agent"))
	}
}
