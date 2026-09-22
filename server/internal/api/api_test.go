package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
)

func Test_UsageCollection_returns_cached_enabled_providers_when_requested(t *testing.T) {
	// Given
	cache := newFakeCache(entry("Claude", 0, 100, nil))
	handler := api.NewHandler(api.Options{Cache: cache, ProviderNames: []string{"Claude"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusOK)
	assertJSON(t, rec, `[{"provider_name":"Claude","primary_label":"Current","secondary_label":"Weekly","show_secondary":true,"subtitle":null,"primary_status_text":null,"secondary_status_text":null,"reauth_command":null,"current":{"utilization":0,"resets_at":null},"weekly":{"utilization":100,"resets_at":null},"buckets":[{"id":"session","label":"Current","utilization":0,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null},{"id":"weekly","label":"Weekly","utilization":100,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null}],"error":null,"needs_reauth":false,"is_success":true,"rate_limit_reset_credits":null}]`)
	if cache.refetches != 0 {
		t.Fatalf("GET performed upstream refetches = %d, want 0", cache.refetches)
	}
}

func Test_ProviderUsage_returns_json_404_when_provider_unknown_or_disabled(t *testing.T) {
	// Given
	handler := api.NewHandler(api.Options{Cache: newFakeCache(), ProviderNames: []string{"Claude"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/cursor", nil)

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusNotFound)
	assertJSON(t, rec, `{"error":"provider not found"}`)
}

func Test_ProviderUsage_returns_cached_provider_when_present(t *testing.T) {
	// Given
	handler := api.NewHandler(api.Options{Cache: newFakeCache(entry("Cursor", 25, 75, nil)), ProviderNames: []string{"Cursor"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/cursor", nil)

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusOK)
	assertJSON(t, rec, `{"provider_name":"Cursor","primary_label":"Current","secondary_label":"Weekly","show_secondary":true,"subtitle":null,"primary_status_text":null,"secondary_status_text":null,"reauth_command":null,"current":{"utilization":25,"resets_at":null},"weekly":{"utilization":75,"resets_at":null},"buckets":[{"id":"session","label":"Current","utilization":25,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null},{"id":"weekly","label":"Weekly","utilization":75,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null}],"error":null,"needs_reauth":false,"is_success":true,"rate_limit_reset_credits":null}`)
}

func Test_Health_returns_version_and_provider_states_when_cache_empty(t *testing.T) {
	// Given
	handler := api.NewHandler(api.Options{Cache: newFakeCache(), Version: "test-version", ProviderNames: []string{"Cursor", "Claude"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusOK)
	assertJSON(t, rec, `{"status":"degraded","version":"test-version","providers":[{"name":"Claude","ok":false,"fetched_at":null},{"name":"Cursor","ok":false,"fetched_at":null}]}`)
}

func Test_Auth_rejects_missing_or_invalid_bearer_when_token_configured(t *testing.T) {
	// Given
	handler := api.NewHandler(api.Options{Cache: newFakeCache(), AuthToken: "secret", ProviderNames: []string{"Claude"}})

	for _, auth := range []string{"", "Bearer wrong", "Basic secret"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", auth)

		// When
		handler.ServeHTTP(rec, req)

		// Then
		assertStatus(t, rec, http.StatusUnauthorized)
		assertJSON(t, rec, `{"error":"unauthorized"}`)
	}
}

func Test_Auth_allows_valid_bearer_and_skips_when_token_empty(t *testing.T) {
	// Given
	authed := api.NewHandler(api.Options{Cache: newFakeCache(), AuthToken: "secret", ProviderNames: []string{"Claude"}})
	open := api.NewHandler(api.Options{Cache: newFakeCache(), ProviderNames: []string{"Claude"}})

	// When
	authRec := httptest.NewRecorder()
	authReq := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	authReq.Header.Set("Authorization", "Bearer secret")
	authed.ServeHTTP(authRec, authReq)
	openRec := httptest.NewRecorder()
	open.ServeHTTP(openRec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	// Then
	assertStatus(t, authRec, http.StatusOK)
	assertStatus(t, openRec, http.StatusOK)
}

func Test_PanicRecovery_returns_sanitized_json_when_handler_panics(t *testing.T) {
	// Given
	handler := api.NewHandler(api.Options{Cache: panicCache{}, ProviderNames: []string{"Claude"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusInternalServerError)
	assertJSON(t, rec, `{"error":"internal error"}`)
	if strings.Contains(rec.Body.String(), "secret panic") {
		t.Fatalf("panic response leaked panic value: %s", rec.Body.String())
	}
}
