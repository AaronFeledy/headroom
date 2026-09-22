package usage_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Bucket_MarshalJSON_emits_ordered_contract_with_UTC_reset(t *testing.T) {
	// Given
	reset := time.Date(2026, 7, 12, 18, 30, 0, 0, time.FixedZone("offset", -4*60*60))
	bucket := usage.Bucket{ID: "five_hour", Label: "5-Hour", Utilization: 42.5, ResetsAt: &reset}

	// When
	encoded, err := json.Marshal(bucket)

	// Then
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	want := `{"id":"five_hour","label":"5-Hour","utilization":42.5,"resets_at":"2026-07-12T22:30:00Z","status_text":null,"starts_at":null,"detail_text":null}`
	if string(encoded) != want {
		t.Fatalf("JSON = %s, want %s", encoded, want)
	}
}

func Test_UsageData_MarshalJSON_emits_empty_buckets_array_in_contract_position(t *testing.T) {
	// Given
	data := usage.UsageData{ProviderName: "Claude"}

	// When
	encoded, err := json.Marshal(data)

	// Then
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertUsageKeys(t, encoded)
	assertJSONField(t, encoded, "buckets", `[]`)
	want := `"weekly":{"utilization":0,"resets_at":null},"buckets":[],"error":null`
	if !bytes.Contains(encoded, []byte(want)) {
		t.Fatalf("JSON = %s, want ordered fragment %s", encoded, want)
	}
}

func Test_UsageData_MarshalJSON_forces_empty_buckets_when_error_present(t *testing.T) {
	// Given
	errText := "provider fetch failed"
	data := usage.FromBuckets("Claude", []usage.Bucket{
		{ID: usage.BucketSession, Label: "Current", Utilization: 42},
		{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 17},
	})
	data.Error = &errText

	// When
	encoded, err := json.Marshal(data)

	// Then
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	assertJSONField(t, encoded, "buckets", `[]`)
	assertJSONField(t, encoded, "is_success", `false`)
}

func Test_DeriveHeader_selects_first_bucket_and_weekly_ID(t *testing.T) {
	// Given
	current := usage.Bucket{ID: "five_hour", Label: "5-Hour", Utilization: 10}
	daily := usage.Bucket{ID: "daily", Label: "Daily", Utilization: 20}
	weekly := usage.Bucket{ID: "weekly", Label: "7-Day", Utilization: 30}

	// When
	got := usage.DeriveHeader([]usage.Bucket{current, daily, weekly})

	// Then
	if got.Current.Utilization != current.Utilization || got.Weekly.Utilization != weekly.Utilization {
		t.Fatalf("header buckets = current %.1f weekly %.1f, want %.1f and %.1f", got.Current.Utilization, got.Weekly.Utilization, current.Utilization, weekly.Utilization)
	}
	if !got.ShowSecondary || got.PrimaryLabel != "5-Hour" || got.SecondaryLabel != "7-Day" {
		t.Fatalf("header labels = (%q, %q, %t), want (%q, %q, true)", got.PrimaryLabel, got.SecondaryLabel, got.ShowSecondary, "5-Hour", "7-Day")
	}
}

func Test_DeriveHeader_uses_documented_fallbacks_when_weekly_ID_missing(t *testing.T) {
	tests := []struct {
		name          string
		buckets       []usage.Bucket
		wantCurrent   float64
		wantWeekly    float64
		wantSecondary bool
		wantPrimary   string
		wantLabel     string
	}{
		{name: "empty", buckets: nil, wantLabel: "Weekly"},
		{name: "one bucket", buckets: []usage.Bucket{{ID: "session", Label: "Session", Utilization: 11}}, wantCurrent: 11, wantPrimary: "Session", wantLabel: "Weekly"},
		{name: "second bucket", buckets: []usage.Bucket{{ID: "session", Label: "Session", Utilization: 11}, {ID: "month", Label: "Monthly", Utilization: 22}}, wantCurrent: 11, wantWeekly: 22, wantSecondary: true, wantPrimary: "Session", wantLabel: "Monthly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := usage.DeriveHeader(tt.buckets)

			// Then
			if got.Current.Utilization != tt.wantCurrent || got.Weekly.Utilization != tt.wantWeekly || got.ShowSecondary != tt.wantSecondary || got.PrimaryLabel != tt.wantPrimary || got.SecondaryLabel != tt.wantLabel {
				t.Fatalf("DeriveHeader() = %+v, want current %.1f weekly %.1f secondary %t labels %q/%q", got, tt.wantCurrent, tt.wantWeekly, tt.wantSecondary, tt.wantPrimary, tt.wantLabel)
			}
		})
	}
}

func Test_UsageData_WithBuckets_overwrites_header_and_preserves_provider_metadata(t *testing.T) {
	// Given
	subtitle, primaryStatus, secondaryStatus, command, errText := "Max", "Soon", "Later", "claude", "rate limited"
	data := usage.UsageData{
		ProviderName: "Claude", PrimaryLabel: "old primary", SecondaryLabel: "old secondary", ShowSecondary: false,
		Subtitle: &subtitle, PrimaryStatusText: &primaryStatus, SecondaryStatusText: &secondaryStatus,
		ReauthCommand: &command, Current: usage.UsageBucket{Utilization: 90}, Weekly: usage.UsageBucket{Utilization: 91},
		Error: &errText, NeedsReauth: true,
	}
	buckets := []usage.Bucket{{ID: "session", Label: "Session", Utilization: 12}, {ID: "weekly", Label: "Weekly", Utilization: 34}}

	// When
	got := data.WithBuckets(buckets)

	// Then
	if !reflect.DeepEqual(got.Buckets, buckets) || got.Current.Utilization != 12 || got.Weekly.Utilization != 34 || !got.ShowSecondary || got.PrimaryLabel != "Session" || got.SecondaryLabel != "Weekly" {
		t.Fatalf("WithBuckets() header = %+v", got)
	}
	if got.ProviderName != data.ProviderName || got.Subtitle != data.Subtitle || got.PrimaryStatusText != data.PrimaryStatusText || got.SecondaryStatusText != data.SecondaryStatusText || got.ReauthCommand != data.ReauthCommand || got.Error != data.Error || got.NeedsReauth != data.NeedsReauth {
		t.Fatalf("WithBuckets() did not preserve metadata: got %+v, source %+v", got, data)
	}
}

func TestBucketMetadataSerializesUTCAndRejectsInvalidWindow(t *testing.T) {
	start := time.Date(2026, 7, 7, 4, 0, 0, 0, time.FixedZone("offset", -5*60*60))
	end := start.Add(7 * 24 * time.Hour)
	detail := "Prepaid balance: $12.50"
	bucket := usage.Bucket{ID: "weekly", Label: "Weekly", StartsAt: &start, ResetsAt: &end, DetailText: &detail}
	encoded, err := json.Marshal(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"starts_at":"2026-07-07T09:00:00Z"`)) || !bytes.Contains(encoded, []byte(`"detail_text":"Prepaid balance: $12.50"`)) {
		t.Fatalf("metadata missing: %s", encoded)
	}
	bucket.StartsAt = &end
	encoded, err = json.Marshal(bucket)
	if err != nil || !bytes.Contains(encoded, []byte(`"starts_at":null`)) {
		t.Fatalf("invalid window accepted: %s %v", encoded, err)
	}
}
