package grok

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Provider_refresh_adopts_completed_sibling_rotation_without_token_call(t *testing.T) {
	// Given stale credentials on disk and a token endpoint that must never be hit
	const staleJSON = `{"https://auth.x.ai/oauth2/token":{"key":"stale-token","refresh_token":"stale-refresh","expires_at":1}}`
	authPath := writeAuthFile(t, staleJSON)
	stale, err := parseCredentials([]byte(staleJSON))
	if err != nil {
		t.Fatalf("parseCredentials: %v", err)
	}
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			tokenCalls.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), TokenURL: server.URL + "/oauth2/token", Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	rotatedJSON := `{"https://auth.x.ai/oauth2/token":{"key":"rotated-token","refresh_token":"rotated-refresh","expires_at":1800000000000}}`
	if err := credstore.AtomicUpdate(context.Background(), authPath, func(_ []byte) ([]byte, error) {
		return []byte(rotatedJSON), nil
	}); err != nil {
		t.Fatalf("sibling credential rotation: %v", err)
	}

	// When refresh runs with the credentials it started from, now superseded on disk
	adopted, err := provider.refresh(context.Background(), stale, refreshReasonUnauthorized)

	// Then it adopts the sibling's completed rotation without a token request
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0 while adopting sibling rotation", got)
	}
	if adopted.accessToken != "rotated-token" || adopted.refreshToken != "rotated-refresh" {
		t.Fatalf("adopted credentials = %q/%q, want rotated-token/rotated-refresh", adopted.accessToken, adopted.refreshToken)
	}
	onDisk, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	persisted, err := parseCredentials(onDisk)
	if err != nil {
		t.Fatalf("parse persisted credentials: %v", err)
	}
	if persisted.accessToken != "rotated-token" || persisted.refreshToken != "rotated-refresh" {
		t.Fatalf("persisted credentials = %q/%q, want rotated-token/rotated-refresh", persisted.accessToken, persisted.refreshToken)
	}
}

func Test_Provider_Fetch_does_not_preserve_expired_timestamp_when_refresh_omits_expiry(t *testing.T) {
	// Given
	const entryKey = "https://auth.x.ai/oauth2/token"
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":"2000-01-01T00:00:00Z"}}`)
	server := newGrokTestServer(t, grokTestHooks{tokenBody: `{"access_token":"new-token","refresh_token":"new-refresh"}`})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	bytes, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]map[string]json.RawMessage
	if err := json.Unmarshal(bytes, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if raw, ok := root[entryKey]["expires_at"]; ok {
		t.Fatalf("expires_at = %s, want field cleared", raw)
	}
	if got := string(root[entryKey]["create_time"]); got != `"2023-11-14T22:13:20Z"` {
		t.Fatalf("create_time = %s, want fresh refresh timestamp", got)
	}
}

func Test_Provider_Fetch_retries_coded_transient_token_error(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			if tokenCalls.Add(1) == 1 {
				http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`))
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token", Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want retry success", err, data.Error)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token endpoint calls = %d, want 2", got)
	}
}

func Test_Provider_Fetch_does_not_retry_invalid_grant(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"bad-refresh","expires_at":1}}`)
	var tokenCalls atomic.Int32
	var billingCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			billingCalls.Add(1)
			http.NotFound(w, r)
			return
		}
		tokenCalls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), TokenURL: server.URL + "/oauth2/token", BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web", Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !data.NeedsReauth {
		t.Fatalf("NeedsReauth = false, want true")
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
	if got := billingCalls.Load(); got != 0 {
		t.Fatalf("billing calls after invalid grant = %d", got)
	}
}

func Test_Provider_Fetch_does_not_retry_invalid_client(t *testing.T) {
	// Given
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		tokenCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(Options{CredentialsPath: writeAuthFile(t, `{}`), HTTPClient: server.Client(), TokenURL: server.URL + "/oauth2/token", Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	_, err = provider.requestRefresh(context.Background(), credentials{refreshToken: "bad-refresh"})

	// Then
	if !errors.Is(err, errInvalidGrant) {
		t.Fatalf("requestRefresh error = %v, want errInvalidGrant", err)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
}

func Test_credentials_needsRefresh_uses_30_day_create_time_fallback(t *testing.T) {
	// Given
	created := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	creds, err := parseCredentials([]byte(`{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","create_time":"2026-07-01T12:00:00Z"}}`))
	if err != nil {
		t.Fatalf("parseCredentials: %v", err)
	}

	// When
	beforeWindow := creds.needsRefresh(created.Add(30*24*time.Hour - refreshBuffer - time.Second))
	atWindow := creds.needsRefresh(created.Add(30*24*time.Hour - refreshBuffer))

	// Then
	if beforeWindow {
		t.Fatalf("needsRefresh before fallback window = true, want false")
	}
	if !atWindow {
		t.Fatalf("needsRefresh at fallback window = false, want true")
	}
}
