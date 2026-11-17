package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type healthWire struct {
	Status    string `json:"status"`
	Providers []struct {
		Name            string `json:"name"`
		OK              bool   `json:"ok"`
		AccountPresence string `json:"account_presence"`
	} `json:"providers"`
}

func Test_Health_keeps_status_ok_for_failing_absent_providers(t *testing.T) {
	failed := entry("Grok", 0, 0, strPtr("signed out"))
	failed.Data.AccountPresence = usage.PresenceAbsent
	healthy := entry("Claude", 30, 70, nil)
	healthy.Data.AccountPresence = usage.PresencePresent
	handler := api.NewHandler(api.Options{Cache: newFakeCache(failed, healthy), ProviderNames: []string{"Claude", "Grok"}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	assertStatus(t, rec, http.StatusOK)
	var got healthWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok for a provider without an account", got.Status)
	}
	if len(got.Providers) != 2 || got.Providers[0].AccountPresence != usage.PresencePresent || got.Providers[1].AccountPresence != usage.PresenceAbsent {
		t.Fatalf("providers = %+v, want present Claude and absent Grok", got.Providers)
	}
}

func Test_Health_degrades_for_a_failing_present_provider(t *testing.T) {
	failed := entry("Grok", 0, 0, strPtr("upstream failed"))
	failed.Data.AccountPresence = usage.PresencePresent
	handler := api.NewHandler(api.Options{Cache: newFakeCache(failed), ProviderNames: []string{"Grok"}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	assertStatus(t, rec, http.StatusOK)
	var got healthWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Status != "degraded" {
		t.Fatalf("status = %q, want degraded for a failing provider with an account", got.Status)
	}
}

func Test_Health_reports_present_for_a_provider_not_polled_yet(t *testing.T) {
	handler := api.NewHandler(api.Options{Cache: newFakeCache(), ProviderNames: []string{"Cursor"}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	var got healthWire
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if len(got.Providers) != 1 || got.Providers[0].AccountPresence != usage.PresencePresent {
		t.Fatalf("providers = %+v, want an unpolled entry reported present", got.Providers)
	}
}

func Test_ProviderUsage_wire_includes_account_presence_on_success_and_error_rows(t *testing.T) {
	success := entry("Cursor", 10, 20, nil)
	success.Data.AccountPresence = usage.PresencePresent
	failed := entry("Grok", 0, 0, strPtr("rate limited"))
	failed.Data.AccountPresence = usage.PresenceAbsent
	for _, tc := range []struct {
		name string
		row  poller.Entry
		want string
	}{
		{"success row", success, `"present"`},
		{"error row", failed, `"absent"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := api.NewHandler(api.Options{Cache: newFakeCache(tc.row), ProviderNames: []string{tc.row.Data.ProviderName}})
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/"+tc.row.Data.ProviderName, nil))

			assertStatus(t, rec, http.StatusOK)
			var got map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode usage: %v", err)
			}
			if string(got["account_presence"]) != tc.want {
				t.Fatalf("account_presence = %s, want %s", got["account_presence"], tc.want)
			}
		})
	}
}

func strPtr(value string) *string { return &value }
