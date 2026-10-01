package usage_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_UsageData_MarshalJSON_matches_windows_contract_when_successful(t *testing.T) {
	// Given
	reset := time.Date(2026, 7, 12, 18, 30, 0, 0, time.FixedZone("offset", -4*60*60))
	subtitle := "Max"
	primaryStatus := "Resets soon"
	data := usage.UsageData{
		ProviderName:        "Claude",
		PrimaryLabel:        "Current Session",
		SecondaryLabel:      "Weekly",
		ShowSecondary:       true,
		Subtitle:            &subtitle,
		PrimaryStatusText:   &primaryStatus,
		SecondaryStatusText: nil,
		ReauthCommand:       nil,
		Current:             usage.UsageBucket{Utilization: 42.5, ResetsAt: &reset},
		Weekly:              usage.UsageBucket{Utilization: 71, ResetsAt: nil},
		Error:               nil,
		NeedsReauth:         false,
	}

	// When
	encoded, err := json.Marshal(data)

	// Then
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertUsageKeys(t, encoded)
	assertJSONField(t, encoded, "is_success", `true`)
	assertJSONField(t, encoded, "subtitle", `"Max"`)
	assertJSONField(t, encoded, "secondary_status_text", `null`)
	assertJSONField(t, encoded, "reauth_command", `null`)
	assertAbsentJSONField(t, encoded, "time_until_reset")
	assertJSONField(t, encoded, "current.resets_at", `"2026-07-12T22:30:00Z"`)
	assertJSONField(t, encoded, "weekly.resets_at", `null`)
}

func Test_UsageData_MarshalJSON_computes_success_from_error_when_error_present(t *testing.T) {
	// Given
	errText := "rate limited"
	data := usage.UsageData{ProviderName: "Claude", Error: &errText, NeedsReauth: true}

	// When
	encoded, err := json.Marshal(data)

	// Then
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertJSONField(t, encoded, "error", `"rate limited"`)
	assertJSONField(t, encoded, "is_success", `false`)
}

func Test_UsageData_MarshalJSON_emits_optional_reset_credit_metadata(t *testing.T) {
	fingerprint := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data := usage.UsageData{ProviderName: "Codex", RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 3, AccountFingerprint: &fingerprint}}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertJSONField(t, encoded, "rate_limit_reset_credits.available_count", `3`)
	assertJSONField(t, encoded, "rate_limit_reset_credits.account_fingerprint", `"`+fingerprint+`"`)

	errorText := "unavailable"
	data.Error = &errorText
	encoded, err = json.Marshal(data)
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertJSONField(t, encoded, "rate_limit_reset_credits", `null`)
}

func Test_RefreshResult_String_returns_stable_wire_text_for_all_variants(t *testing.T) {
	tests := []struct {
		name string
		in   usage.RefreshResult
		want string
	}{
		{name: "success", in: usage.RefreshSuccess, want: "success"},
		{name: "invalid grant", in: usage.RefreshInvalidGrant, want: "invalid_grant"},
		{name: "failed", in: usage.RefreshFailed, want: "failed"},
		{name: "unknown", in: usage.RefreshResult(99), want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := tt.in.String()

			// Then
			if got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_Provider_interface_accepts_minimal_fetcher_contract(t *testing.T) {
	// Given
	provider := fakeProvider{name: "Claude"}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	var typed usage.Provider = provider
	if typed.Name() != "Claude" {
		t.Fatalf("Name() = %q, want Claude", typed.Name())
	}
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.ProviderName != "Claude" {
		t.Fatalf("ProviderName = %q, want Claude", data.ProviderName)
	}
}

type fakeProvider struct{ name string }

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) Fetch(_ context.Context) (usage.UsageData, error) {
	return usage.UsageData{ProviderName: p.name}, nil
}

func assertUsageKeys(t *testing.T, encoded []byte) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode usage json: %v", err)
	}
	want := []string{
		"provider_name",
		"primary_label",
		"secondary_label",
		"show_secondary",
		"subtitle",
		"primary_status_text",
		"secondary_status_text",
		"reauth_command",
		"current",
		"weekly",
		"buckets",
		"error",
		"needs_reauth",
		"is_success",
		"rate_limit_reset_credits",
		"auth",
	}
	if !reflect.DeepEqual(sortedKeys(got), want) {
		t.Fatalf("keys = %v, want %v", sortedKeys(got), want)
	}
}

func assertJSONField(t *testing.T, encoded []byte, path string, want string) {
	t.Helper()
	got, err := jsonField(encoded, path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %s, want %s", path, got, want)
	}
}

func assertAbsentJSONField(t *testing.T, encoded []byte, key string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode usage json: %v", err)
	}
	if _, ok := got[key]; ok {
		t.Fatalf("%s key is present in %s", key, string(encoded))
	}
}

func jsonField(encoded []byte, path string) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, err
	}
	if _, _, nested := cutPath(path); nested {
		bucketName, fieldName, _ := cutPath(path)
		var bucket map[string]json.RawMessage
		if err := json.Unmarshal(root[bucketName], &bucket); err != nil {
			return nil, err
		}
		return bucket[fieldName], nil
	}
	return root[path], nil
}

func cutPath(path string) (string, string, bool) {
	for i, r := range path {
		if r == '.' {
			return path[:i], path[i+1:], true
		}
	}
	return path, "", false
}

func sortedKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	order := map[string]int{
		"provider_name": 0, "primary_label": 1, "secondary_label": 2, "show_secondary": 3,
		"subtitle": 4, "primary_status_text": 5, "secondary_status_text": 6, "reauth_command": 7,
		"current": 8, "weekly": 9, "buckets": 10, "error": 11, "needs_reauth": 12, "is_success": 13,
		"rate_limit_reset_credits": 14,
		"auth":                     15,
	}
	for key := range values {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if order[keys[j]] < order[keys[i]] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
