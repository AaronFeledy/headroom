package claude

import (
	"reflect"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_parseUsageResponse_CLD1_builds_dynamic_buckets_for_Fable_limits(t *testing.T) {
	// Given
	body := []byte(`{"limits":[{"kind":"session","percent":12},{"kind":"weekly_all","percent":34},{"kind":"weekly_scoped","percent":56,"scope":{"model":{"display_name":"Fable"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 12}, {"weekly", "Weekly", 34}, {"weekly_fable", "Fable", 56}})
}

func Test_parseUsageResponse_CLD2_accepts_limits_without_legacy_windows(t *testing.T) {
	// Given
	body := []byte(`{"five_hour":null,"seven_day":null,"limits":[{"kind":"session","percent":23},{"kind":"weekly_all","percent":45}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 23}, {"weekly", "Weekly", 45}})
}

func Test_parseUsageResponse_CLD3_backfills_legacy_session_and_weekly(t *testing.T) {
	// Given
	body := []byte(`{"five_hour":{"utilization":11},"seven_day":{"utilization":22}}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 11}, {"weekly", "Weekly", 22}})
}

func Test_parseUsageResponse_CLD4_includes_inactive_scoped_limit_with_usage(t *testing.T) {
	// Given
	body := []byte(`{"limits":[{"kind":"session","percent":10},{"kind":"weekly_scoped","percent":20,"is_active":false,"scope":{"model":{"display_name":"Fable"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 10}, {"weekly_fable", "Fable", 20}})
}

func Test_parseUsageResponse_prefers_active_header_limits_and_keeps_inactive_scoped_usage(t *testing.T) {
	// Given
	body := []byte(`{"limits":[{"kind":"session","percent":10,"is_active":false},{"kind":"session","percent":11,"is_active":true},{"kind":"weekly_all","percent":20,"is_active":false},{"kind":"weekly_all","percent":21,"is_active":true},{"kind":"weekly_scoped","percent":30,"is_active":false,"scope":{"model":{"display_name":"Fable"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 11}, {"weekly", "Weekly", 21}, {"weekly_fable", "Fable", 30}})
}

func Test_parseUsageResponse_CLD5_appends_legacy_model_buckets(t *testing.T) {
	// Given
	body := []byte(`{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"seven_day_sonnet":{"utilization":30},"seven_day_opus":{"utilization":40}}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 10}, {"weekly", "Weekly", 20}, {"weekly_sonnet", "Sonnet", 30}, {"weekly_opus", "Opus", 40}})
}

func Test_parseUsageResponse_CLD6_deduplicates_scoped_and_legacy_sonnet(t *testing.T) {
	// Given
	body := []byte(`{"seven_day_sonnet":{"utilization":99},"limits":[{"kind":"session","percent":10},{"kind":"weekly_scoped","percent":30,"scope":{"model":{"display_name":"Sonnet"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 10}, {"weekly_sonnet", "Sonnet", 30}})
}

func Test_parseUsageResponse_CLD7_slugifies_scoped_model_name(t *testing.T) {
	// Given
	body := []byte(`{"limits":[{"kind":"session","percent":10},{"kind":"weekly_scoped","percent":20,"scope":{"model":{"display_name":"Claude 3.5 Sonnet"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 10}, {"weekly_claude_3_5_sonnet", "Claude 3.5 Sonnet", 20}})
}

func Test_parseUsageResponse_CLD8_accepts_limits_without_session(t *testing.T) {
	// Given
	body := []byte(`{"limits":[{"kind":"weekly_all","percent":20},{"kind":"weekly_scoped","percent":30,"scope":{"model":{"display_name":"Fable"}}}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"weekly", "Weekly", 20}, {"weekly_fable", "Fable", 30}})
	if got.SessionFromFiveHour {
		t.Fatal("weekly-only limits reported as a five-hour session")
	}
}

func Test_parseUsageResponse_Ultra_weekly_only_shape_omits_session(t *testing.T) {
	// Given the real weekly-only shape: no session entry, one active weekly and
	// one inactive scoped Fable weekly.
	body := []byte(`{"five_hour":null,"seven_day":{"utilization":41,"resets_at":"2026-11-13T00:00:00Z"},"seven_day_oauth_apps":null,"seven_day_opus":null,"seven_day_sonnet":null,"extra_usage":{"is_enabled":false,"monthly_limit":0,"used_credits":0,"utilization":null},"limits":[{"kind":"weekly_all","group":"weekly","percent":41,"resets_at":"2026-11-13T00:00:00Z","scope":null,"is_active":true},{"kind":"weekly_scoped","group":"weekly","percent":17,"resets_at":"2026-11-13T00:00:00Z","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]}`)

	// When
	got, err := parseUsageResponse(body, nil)

	// Then
	if err != nil {
		t.Fatalf("parseUsageResponse returned error: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"weekly", "Weekly", 41}, {"weekly_fable", "Fable", 17}})
	if got.SessionFromFiveHour {
		t.Fatal("session window reported for a weekly-only response")
	}
}

func Test_parseUsageResponse_selects_primary_window_and_clamps_utilization(t *testing.T) {
	tests := []struct {
		name, body, wantLabel   string
		wantCurrent, wantWeekly float64
		wantIDs                 []string
		wantErr                 bool
	}{
		{name: "normal five hour and seven day windows", body: `{"five_hour":{"utilization":42.5},"seven_day":{"utilization":71}}`, wantCurrent: 42.5, wantWeekly: 71, wantLabel: "Current Session", wantIDs: []string{"session", "weekly"}},
		{name: "null five hour reports only the weekly window", body: `{"five_hour":null,"seven_day":{"utilization":71}}`, wantCurrent: 71, wantWeekly: 71, wantLabel: "Weekly", wantIDs: []string{"weekly"}},
		{name: "null five hour and seven day uses oauth apps for weekly", body: `{"five_hour":null,"seven_day":null,"seven_day_oauth_apps":{"utilization":55}}`, wantCurrent: 55, wantWeekly: 55, wantLabel: "Weekly", wantIDs: []string{"weekly"}},
		{name: "all windows null returns error", body: `{"five_hour":null,"seven_day":null,"seven_day_oauth_apps":null,"seven_day_sonnet":null,"seven_day_opus":null}`, wantErr: true},
		{name: "utilization above range clamps to maximum", body: `{"five_hour":{"utilization":150}}`, wantCurrent: 100, wantLabel: "Current Session", wantIDs: []string{"session"}},
		{name: "utilization below range clamps to minimum", body: `{"five_hour":{"utilization":-5}}`, wantCurrent: 0, wantLabel: "Current Session", wantIDs: []string{"session"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got, err := parseUsageResponse([]byte(tt.body), nil)

			// Then
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseUsageResponse error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if err != nil {
				t.Fatalf("parseUsageResponse returned error: %v", err)
			}
			if got.Current.Utilization != tt.wantCurrent || got.Weekly.Utilization != tt.wantWeekly {
				t.Fatalf("utilization = %.1f/%.1f, want %.1f/%.1f", got.Current.Utilization, got.Weekly.Utilization, tt.wantCurrent, tt.wantWeekly)
			}
			if got.PrimaryLabel != tt.wantLabel {
				t.Fatalf("PrimaryLabel = %q, want %q", got.PrimaryLabel, tt.wantLabel)
			}
			if tt.wantIDs != nil {
				ids := make([]string, 0, len(got.Buckets))
				for _, bucket := range got.Buckets {
					ids = append(ids, bucket.ID)
				}
				if !reflect.DeepEqual(ids, tt.wantIDs) {
					t.Fatalf("bucket ids = %v, want %v", ids, tt.wantIDs)
				}
			}
		})
	}
}

type bucketExpectation struct {
	id          string
	label       string
	utilization float64
}

func assertBuckets(t *testing.T, got []usage.Bucket, want []bucketExpectation) {
	t.Helper()
	actual := make([]bucketExpectation, 0, len(got))
	for _, bucket := range got {
		actual = append(actual, bucketExpectation{bucket.ID, bucket.Label, bucket.Utilization})
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("buckets = %#v, want %#v", actual, want)
	}
}

func Test_parseUsageResponse_extra_usage_enabled_zero_spend_shows_meter(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":true,"monthly_limit":1000,"used_credits":0,"utilization":null}}`)
	got, err := parseUsageResponse(body, nil)
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if len(got.Buckets) != 3 || got.Buckets[2].ID != usage.BucketExtra || got.Buckets[2].Label != "Extra usage" {
		t.Fatalf("buckets = %#v", got.Buckets)
	}
	if got.Buckets[2].Utilization != 0 {
		t.Fatalf("extra util = %v, want 0", got.Buckets[2].Utilization)
	}
	if got.Buckets[2].StatusText == nil || *got.Buckets[2].StatusText != "0 / 1000 credits" {
		t.Fatalf("status = %v", got.Buckets[2].StatusText)
	}
}

func Test_parseUsageResponse_extra_usage_disabled_with_spend_shows_meter(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":10},"extra_usage":{"is_enabled":false,"monthly_limit":100,"used_credits":25}}`)
	got, err := parseUsageResponse(body, nil)
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if len(got.Buckets) != 2 || got.Buckets[1].ID != usage.BucketExtra || got.Buckets[1].Utilization != 25 {
		t.Fatalf("buckets = %#v", got.Buckets)
	}
}

func Test_parseUsageResponse_extra_usage_disabled_zero_hidden(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":10},"extra_usage":{"is_enabled":false,"monthly_limit":100,"used_credits":0}}`)
	got, err := parseUsageResponse(body, nil)
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	assertBuckets(t, got.Buckets, []bucketExpectation{{"session", "Current Session", 10}})
}
