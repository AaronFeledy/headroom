package usage_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestUsageDetailsJSONEmitsExplicitNullsWhenUnknown(t *testing.T) {
	// Given
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"details", usage.UsageDetails{}, `{"plan":null,"quotas":null,"finances":null,"credit_grants":null,"reset_inventory":null,"product_usage":null}`},
		{"observation", usage.Observation{State: usage.DetailUnknown}, `{"state":"unknown","observed_at":null}`},
		{"scope", usage.UsageScope{Kind: "unknown"}, `{"kind":"unknown","shared_pool":null}`},
		{"plan", usage.PlanDetails{Observation: usage.Observation{State: usage.DetailUnsupported}}, `{"state":"unsupported","observed_at":null,"label":null,"is_unlimited":null,"scope":null}`},
		{"quota", usage.QuotaDetails{Observation: usage.Observation{State: usage.DetailStale}, BucketID: "plan"}, `{"state":"stale","observed_at":null,"bucket_id":"plan","plan_label":null,"has_available_usage":null,"scope":null}`},
		{"finances", usage.FinancialDetails{}, `{"state":"","observed_at":null,"items":null}`},
		{"financial item", usage.FinancialItem{Currency: "USD"}, `{"currency":"USD","scope":null,"starts_at":null,"resets_at":null,"included_used_minor":null,"included_limit_minor":null,"included_remaining_minor":null,"prepaid_remaining_minor":null,"extra_used_minor":null,"extra_cap_minor":null,"extra_enabled":null,"auto_top_up_enabled":null}`},
		{"grants", usage.CreditGrantDetails{}, `{"state":"","observed_at":null,"items":null}`},
		{"grant", usage.CreditGrant{DisplayName: "Team product bonus", Currency: "USD"}, `{"display_name":"Team product bonus","product":null,"currency":"USD","remaining_minor":null,"total_minor":null,"expires_at":null,"scope":null}`},
		{"inventory", usage.ResetInventoryDetails{}, `{"state":"","observed_at":null,"reported_count":null,"valid_now_count":null,"windows":null}`},
		{"window", usage.ResetWindow{}, `{"count":0,"valid_from":null,"expires_at":null}`},
		{"products", usage.ProductUsageDetails{}, `{"state":"","observed_at":null,"parent_bucket_id":null,"items":null}`},
		{"product", usage.ProductUsageItem{Product: "Grok Code"}, `{"product":"Grok Code","reported_percent":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			encoded, err := json.Marshal(tt.value)
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tt.want {
				t.Fatalf("JSON = %s, want %s", encoded, tt.want)
			}
		})
	}
}

func TestUsageDetailsJSONPreservesEmptyCollectionsWhenKnownEmpty(t *testing.T) {
	// Given
	ok := usage.Observation{State: usage.DetailOK}
	details := usage.UsageDetails{
		Quotas:         []usage.QuotaDetails{},
		Finances:       &usage.FinancialDetails{Observation: ok, Items: []usage.FinancialItem{}},
		CreditGrants:   &usage.CreditGrantDetails{Observation: ok, Items: []usage.CreditGrant{}},
		ResetInventory: &usage.ResetInventoryDetails{Observation: ok, Windows: []usage.ResetWindow{}},
		ProductUsage:   &usage.ProductUsageDetails{Observation: ok, Items: []usage.ProductUsageItem{}},
	}
	// When
	encoded, err := json.Marshal(details)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"quotas", "finances.items", "credit_grants.items", "reset_inventory.windows", "product_usage.items"} {
		assertJSONField(t, encoded, path, `[]`)
	}
}

func TestUsageDetailsJSONPreservesReportedValues(t *testing.T) {
	// Given
	details := populatedDetails()
	// When
	encoded, err := json.Marshal(details)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	var decoded usage.UsageDetails
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&decoded, details) {
		t.Fatal("detail values changed during JSON round trip")
	}
	assertJSONField(t, encoded, "plan.observed_at", `"2026-10-29T12:00:00.123456789Z"`)
	assertJSONField(t, encoded, "plan.is_unlimited", `false`)
	var financialItems []json.RawMessage
	items, err := jsonField(encoded, "finances.items")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(items, &financialItems); err != nil {
		t.Fatal(err)
	}
	if len(financialItems) != 1 {
		t.Fatalf("financial items = %d, want 1", len(financialItems))
	}
	assertJSONField(t, financialItems[0], "included_used_minor", `0`)
	assertJSONField(t, financialItems[0], "included_limit_minor", `9007199254740991`)
	assertJSONField(t, financialItems[0], "auto_top_up_enabled", `false`)
}
