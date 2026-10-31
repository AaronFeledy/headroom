package usage_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestUsageDataJSONEmitsNullDetailsWhenAbsent(t *testing.T) {
	// Given
	data := usage.UsageData{ProviderName: "Example"}
	// When
	encoded, err := json.Marshal(data)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	assertUsageKeys(t, encoded)
	assertJSONField(t, encoded, "usage_details", `null`)
}

func TestUsageDataJSONClearsDetailsWhenProviderFails(t *testing.T) {
	// Given
	marker := "SECRET_METADATA_MARKER"
	errorText := "provider unavailable"
	details := populatedDetails()
	details.Plan.Label = &marker
	data := usage.FromBuckets("Example", []usage.Bucket{{ID: "plan", Label: "Plan", Utilization: 12}})
	data.UsageDetails = details
	data.Error = &errorText
	// When
	encoded, err := json.Marshal(data)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	assertUsageKeys(t, encoded)
	assertJSONField(t, encoded, "usage_details", `null`)
	assertJSONField(t, encoded, "buckets", `[]`)
	assertJSONField(t, encoded, "is_success", `false`)
	if bytes.Contains(encoded, []byte(marker)) {
		t.Fatal("error response leaked suppressed detail metadata")
	}
	if data.UsageDetails != details || *details.Plan.Label != marker {
		t.Fatal("serialization mutated source details")
	}
}

func TestUsageDataJSONKeepsInternalIdentityPrivateWithDetails(t *testing.T) {
	// Given
	const marker = "SECRET_ACCOUNT_MARKER"
	data := usage.UsageData{ProviderName: "Grok", ProviderAccountID: marker, UsageDetails: populatedDetails()}
	// When
	encoded, err := json.Marshal(data)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	assertUsageKeys(t, encoded)
	var decoded struct {
		Details *usage.UsageDetails `json:"usage_details"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Details == nil || decoded.Details.ResetInventory == nil || *decoded.Details.ResetInventory.ReportedCount != 3 {
		t.Fatal("read-only inventory was lost")
	}
	assertJSONField(t, encoded, "rate_limit_reset_credits", `null`)
	if bytes.Contains(encoded, []byte(marker)) {
		t.Fatal("internal account identity leaked into usage JSON")
	}
}

func TestBucketMeasurementPreservesLegacyZeroAndHeaderSemantics(t *testing.T) {
	// Given
	for _, tt := range []struct{ measurement, wire string }{
		{"", `null`},
		{usage.MeasurementPercent, `"percent"`},
		{usage.MeasurementStatus, `"status"`},
	} {
		t.Run(tt.wire, func(t *testing.T) {
			buckets := []usage.Bucket{
				{ID: "weekly", Label: "Weekly", Utilization: 37},
				{ID: "plan", Label: "Plan", Utilization: 0, Measurement: tt.measurement},
			}
			// When
			data := usage.FromBuckets("Example", buckets)
			encoded, err := json.Marshal(data)
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Buckets) != 2 || data.Buckets[0].ID != "plan" || data.Buckets[1].ID != "weekly" {
				t.Fatalf("measurement changed bucket IDs/order: %+v", data.Buckets)
			}
			assertJSONField(t, encoded, "current.utilization", `0`)
			assertJSONField(t, encoded, "weekly.utilization", `37`)
			assertJSONField(t, encoded, "primary_label", `"Plan"`)
			assertJSONField(t, encoded, "secondary_label", `"Weekly"`)
			assertJSONField(t, encoded, "show_secondary", `true`)
			var wire struct {
				Buckets []json.RawMessage `json:"buckets"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Buckets) != 2 {
				t.Fatalf("encoded buckets = %d, want 2", len(wire.Buckets))
			}
			assertJSONField(t, wire.Buckets[0], "utilization", `0`)
			assertJSONField(t, wire.Buckets[0], "measurement", tt.wire)
		})
	}
}
