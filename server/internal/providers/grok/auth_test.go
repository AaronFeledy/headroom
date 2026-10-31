package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_Provider_Fetch_selects_auth_x_ai_entry_when_auth_file_has_multiple_entries(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{
		"https://other.example": {"key": "wrong-token", "refresh_token": "wrong-refresh"},
		"https://auth.x.ai/oauth2/token": {"key": "xai-token", "refresh_token": "xai-refresh", "expires_at": 1800000000000, "extra": {"kept": true}}
	}`)
	server := newGrokTestServer(t, grokTestHooks{})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
	if got := server.billingAuthHeaders(); got != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", got)
	}
	if data.ProviderName != "Grok" || data.PrimaryLabel != "Credits" || data.SecondaryLabel != "Pay as you go" {
		t.Fatalf("labels = %#v", data)
	}
	if data.ShowSecondary {
		t.Fatalf("ShowSecondary = true, want false")
	}
	if data.Current.Utilization != 25 {
		t.Fatalf("Current.Utilization = %v, want 25", data.Current.Utilization)
	}
}

func Test_Provider_Fetch_returns_reauth_when_auth_x_ai_entry_is_missing(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://other.example": {"key": "token", "refresh_token": "refresh"}}`)
	server := newGrokTestServer(t, grokTestHooks{})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error == nil || !strings.Contains(*data.Error, "grok login") {
		t.Fatalf("UsageData.Error = %v, want grok login guidance", data.Error)
	}
	if !data.NeedsReauth {
		t.Fatalf("NeedsReauth = false, want true")
	}
}

func Test_Provider_Fetch_refreshes_expired_token_and_preserves_unrelated_auth_entries(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{
		"https://other.example": {"key": "other-token", "refresh_token": "other-refresh", "untouched": true},
		"https://auth.x.ai/oauth2/token": {"key": "old-token", "refresh_token": "old-refresh", "expires_at": 1, "unknown": "keep"}
	}`)
	server := newGrokTestServer(t, grokTestHooks{})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
	if got := server.tokenForm.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", got)
	}
	if got := server.tokenForm.Get("refresh_token"); got != "old-refresh" {
		t.Fatalf("refresh_token = %q, want old-refresh", got)
	}
	if got := server.tokenForm.Get("client_id"); got != oauthClientID {
		t.Fatalf("client_id = %q, want %q", got, oauthClientID)
	}
	assertAuthFileEntry(t, authPath, "https://other.example", "other-token", "other-refresh", false, false)
	assertAuthFileEntry(t, authPath, "https://auth.x.ai/oauth2/token", "new-token", "new-refresh", true, true)
}

func Test_Provider_Fetch_returns_reauth_when_refresh_token_is_invalid_grant(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "old-token", "refresh_token": "bad-refresh", "expires_at": 1}}`)
	server := newGrokTestServer(t, grokTestHooks{tokenStatus: http.StatusBadRequest, tokenBody: `{"error":"invalid_grant"}`})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !data.NeedsReauth {
		t.Fatalf("NeedsReauth = false, want true")
	}
	if data.Error == nil || !strings.Contains(*data.Error, "grok login") {
		t.Fatalf("UsageData.Error = %v, want grok login guidance", data.Error)
	}
}

func Test_Provider_Fetch_returns_error_when_auth_json_is_malformed(t *testing.T) {
	// Given
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"https://auth.x.ai":`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	server := newGrokTestServer(t, grokTestHooks{})
	provider := newTestProvider(t, authPath, server)

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error == nil || !strings.Contains(*data.Error, "Grok auth") {
		t.Fatalf("UsageData.Error = %v, want malformed auth error", data.Error)
	}
}

func writeAuthFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	return path
}

func assertAuthFileEntry(t *testing.T, path string, entryKey string, wantKey string, wantRefresh string, wantExpires bool, wantUnknown bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var root map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	entry := root[entryKey]
	if string(entry["key"]) != `"`+wantKey+`"` {
		t.Fatalf("%s key = %s, want %q", entryKey, entry["key"], wantKey)
	}
	if string(entry["refresh_token"]) != `"`+wantRefresh+`"` {
		t.Fatalf("%s refresh_token = %s, want %q", entryKey, entry["refresh_token"], wantRefresh)
	}
	rawExpires, hasExpires := entry["expires_at"]
	if hasExpires != wantExpires {
		t.Fatalf("%s expires_at present = %v, want %v", entryKey, hasExpires, wantExpires)
	}
	if wantExpires {
		var expiresAt string
		if err := json.Unmarshal(rawExpires, &expiresAt); err != nil {
			t.Fatalf("%s expires_at is not a JSON string (got %s): %v", entryKey, rawExpires, err)
		}
		if expiresAt == "" {
			t.Fatalf("%s expires_at is empty", entryKey)
		}
	}
	if _, ok := entry["unknown"]; ok != wantUnknown {
		t.Fatalf("%s unknown preserved = %v, want %v", entryKey, ok, wantUnknown)
	}
}

func newTestProvider(t *testing.T, authPath string, server *grokTestServer) *Provider {
	t.Helper()
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: authPath,
		HTTPClient:      server.Client(),
		BillingURL:      server.URL + "/v1/billing",
		SettingsURL:     server.URL + "/v1/settings",
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return provider
}

type grokTestHooks struct {
	tokenStatus int
	tokenBody   string
}

type grokTestServer struct {
	*httptest.Server
	billingHeaders []http.Header
	tokenForm      url.Values
}

func newGrokTestServer(t *testing.T, hooks grokTestHooks) *grokTestServer {
	t.Helper()
	state := &grokTestServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing", func(w http.ResponseWriter, r *http.Request) {
		state.billingHeaders = append(state.billingHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":40},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
	})
	mux.HandleFunc("/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subscription_tier_display":"SuperGrok"}`))
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		state.tokenForm = r.PostForm
		status := hooks.tokenStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := hooks.tokenBody
		if body == "" {
			body = `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	state.Server = httptest.NewServer(mux)
	t.Cleanup(state.Close)
	return state
}

func (s *grokTestServer) billingAuthHeaders() string {
	if len(s.billingHeaders) == 0 {
		return ""
	}
	return s.billingHeaders[0].Get("Authorization")
}
