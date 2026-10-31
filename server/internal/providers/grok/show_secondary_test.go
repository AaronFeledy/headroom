package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Provider_ShowSecondary_is_false_for_base_success_reauth_and_error_states(t *testing.T) {
	// Given
	base := baseUsageData()
	success := fetchGrokUsageForShowSecondary(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`, successGrokServer(t))
	reauth := fetchGrokUsageForShowSecondary(t, `{"https://other.example": {"key": "token", "refresh_token": "refresh"}}`, successGrokServer(t))
	failure := fetchGrokUsageForShowSecondary(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`, failingBillingServer(t))

	// Then
	assertShowSecondaryFalse(t, "base", base)
	assertShowSecondaryFalse(t, "success", success)
	assertShowSecondaryFalse(t, "reauth", reauth)
	assertShowSecondaryFalse(t, "failure", failure)
	if success.Weekly.Utilization != 0 || success.Weekly.ResetsAt == nil {
		t.Fatalf("success Weekly = %#v, want zero placeholder with reset", success.Weekly)
	}
	if success.PrimaryLabel != "Credits" || success.SecondaryLabel != "Pay as you go" {
		t.Fatalf("success labels = %q/%q, want Credits/Pay as you go", success.PrimaryLabel, success.SecondaryLabel)
	}
}

func fetchGrokUsageForShowSecondary(t *testing.T, authJSON string, server *httptest.Server) usage.UsageData {
	t.Helper()
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: writeAuthFile(t, authJSON),
		HTTPClient:      server.Client(),
		BillingURL:      server.URL + "/v1/billing",
		SettingsURL:     server.URL + "/v1/settings",
		TokenURL:        server.URL + "/oauth2/token",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	data, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	return data
}

func successGrokServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":40},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func failingBillingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func assertShowSecondaryFalse(t *testing.T, name string, data usage.UsageData) {
	t.Helper()
	if data.ShowSecondary {
		t.Fatalf("%s ShowSecondary = true, want false", name)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal %s usage: %v", name, err)
	}
	if !strings.Contains(string(encoded), `"show_secondary":false`) {
		t.Fatalf("%s JSON = %s, want show_secondary false", name, encoded)
	}
}
