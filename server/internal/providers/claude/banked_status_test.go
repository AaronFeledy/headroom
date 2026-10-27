package claude

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func Test_BankedStatus_computes_available_and_usable(t *testing.T) {
	// Given
	cases := []struct {
		name, body      string
		count           int64
		usable, unknown bool
	}{
		{"absent", `{}`, 0, false, true},
		{"malformed", `{"cedar_ember":{"eligible":"yes","grants":[]}}`, 0, false, true},
		{"ineligible empty", `{"cedar_ember":{"eligible":false,"grants":[]}}`, 0, false, false},
		{"eligible empty", `{"cedar_ember":{"eligible":true,"grants":[]}}`, 0, false, false},
		{"usable", `{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":false,"usable_now":true}]}}`, 2, true, false},
		{"paused", `{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":true,"usable_now":true}]}}`, 0, false, false},
		{"ineligible credit", `{"cedar_ember":{"eligible":false,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":false,"usable_now":true}]}}`, 2, false, false},
		{"cooldown", `{"cedar_ember":{"eligible":true,"cooldown_until":"2099-01-01T00:00:00Z","next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":false,"usable_now":true}]}}`, 2, false, false},
		{"missing next", `{"cedar_ember":{"eligible":true,"next_grant_id":"b","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":false,"usable_now":true}]}}`, 0, false, true},
		{"bad grant", `{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":-1,"paused":false,"usable_now":true},null,{"id":"bad/id"}]}}`, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When
			credits := parseBankStatus([]byte(tc.body)).credits(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			// Then
			if tc.unknown {
				if credits != nil {
					t.Fatalf("expected unknown credits: %+v", credits)
				}
				return
			}
			if credits == nil || credits.AvailableCount != tc.count || credits.UsableNow == nil || *credits.UsableNow != tc.usable {
				t.Fatalf("credits = %+v, want count=%d usable=%v", credits, tc.count, tc.usable)
			}
		})
	}
}

func Test_BankedStatus_rejects_mistyped_optional_fields(t *testing.T) {
	// Given
	cases := []struct {
		field string
		grant bool
	}{
		{`"label":42`, true},
		{`"starts_at":42`, true},
		{`"ends_at":42`, true},
		{`"clears":42`, true},
		{`"use_requires_limit":42`, true},
		{`"percent_used":42`, true},
		{`"blocking":42`, true},
		{`"gives_back_weekly":42`, true},
		{`"unavailable_reason":42`, true},
		{`"ineligible_reason":42`, false},
		{`"at_limit":42`, false},
		{`"exhausted":42`, false},
		{`"weekly_resets_at":42`, false},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			grant := `{"id":"a","resets_total":2,"resets_left":2,"paused":false,"usable_now":true`
			block := `{"eligible":true,"next_grant_id":"a"`
			if tc.grant {
				grant += "," + tc.field
			} else {
				block += "," + tc.field
			}
			body := `{"cedar_ember":` + block + `,"grants":[` + grant + `}]}}`
			// When
			status := parseBankStatus([]byte(body))
			// Then
			if status != nil {
				t.Fatal("mistyped field must invalidate the entire status")
			}
		})
	}
}

func Test_BankedStatus_checks_overflow_before_exposing_status(t *testing.T) {
	// Given
	cases := []struct {
		name     string
		left     int64
		paused   bool
		overflow bool
	}{
		{"maximum", math.MaxInt64 - 1, false, false},
		{"overflow", math.MaxInt64, false, true},
		{"paused maximum", math.MaxInt64, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":1,"resets_left":1,"paused":false,"usable_now":true},{"id":"b","resets_total":9223372036854775807,"resets_left":%d,"paused":%t,"usable_now":true}]}}`, tc.left, tc.paused)
			// When
			status := parseBankStatus([]byte(body))
			// Then
			if tc.overflow {
				if status != nil {
					t.Fatal("overflow must invalidate status, not just credits")
				}
				return
			}
			want := int64(math.MaxInt64)
			if tc.paused {
				want = 1
			}
			credits := status.credits(time.Time{})
			if credits == nil || credits.AvailableCount != want || credits.UsableNow == nil || !*credits.UsableNow {
				t.Fatalf("credits = %+v, want count=%d usable=true", credits, want)
			}
		})
	}
}
