package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Usage_and_health_include_claude_account_rows(t *testing.T) {
	// Given
	defaultEntry := entry("Claude", 1, 2, nil)
	accountEntry := entry("Claude (Work)", 3, 4, nil)
	accountEntry.Data.ProviderType = "Claude"
	accountEntry.Data.Account = &usage.Account{ID: "work", Label: "Work"}
	cache := newFakeCache(defaultEntry, accountEntry)
	handler := api.NewHandler(api.Options{
		Cache:         cache,
		ProviderNames: []string{"Claude", "Claude (Work)"},
	})

	// When / Then usage collection
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil))
	assertStatus(t, rec, http.StatusOK)
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	names := map[string]bool{}
	for _, row := range rows {
		var name string
		if err := json.Unmarshal(row["provider_name"], &name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
		if _, ok := row["provider_type"]; !ok {
			t.Fatal("missing provider_type")
		}
		if _, ok := row["account"]; !ok {
			t.Fatal("missing account")
		}
	}
	if !names["Claude"] || !names["Claude (Work)"] {
		t.Fatalf("names = %v", names)
	}

	// single provider by URL-encoded name
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usage/claude%20(work)", nil))
	assertStatus(t, rec, http.StatusOK)
	var one map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if string(one["provider_name"]) != `"Claude (Work)"` {
		t.Fatalf("provider_name = %s", one["provider_name"])
	}
	if string(one["provider_type"]) != `"Claude"` {
		t.Fatalf("provider_type = %s", one["provider_type"])
	}
	if string(one["account"]) != `{"id":"work","label":"Work"}` {
		t.Fatalf("account = %s", one["account"])
	}

	// health lists both
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	assertStatus(t, rec, http.StatusOK)
	assertJSON(t, rec, `{"status":"ok","version":"dev","providers":[{"name":"Claude","ok":true,"fetched_at":"2026-07-12T10:00:00Z"},{"name":"Claude (Work)","ok":true,"fetched_at":"2026-07-12T10:00:00Z"}]}`)
}
