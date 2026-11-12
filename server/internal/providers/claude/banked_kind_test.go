package claude

import (
	"testing"
	"time"
)

// fixtureBankStatus is the observed live shape with identifiers replaced.
const fixtureBankStatus = `{"cedar_ember":{"eligible":true,"ineligible_reason":null,"at_limit":false,"exhausted":[],"grants":[{"id":"grant_a","label":"One full limit reset (November 2026)","resets_total":1,"resets_left":1,"starts_at":"2026-11-10T21:30:00+00:00","ends_at":"2026-12-10T22:00:00+00:00","clears":["seven_day","seven_day_overage_included"],"paused":false,"usable_now":true,"use_requires_limit":false,"percent_used":{"seven_day":43,"seven_day_overage_included":21},"blocking":[],"arm":null,"type":"weekly","unavailable_reason":null,"gives_back_weekly":false},{"id":"grant_b","label":"New model launch: one Fable reset for Max, Team Premium and Enterprise Premium","resets_total":1,"resets_left":0,"starts_at":"2026-11-09T18:00:00+00:00","ends_at":"2027-03-09T07:59:00+00:00","clears":["seven_day_overage_included"],"paused":false,"usable_now":false,"use_requires_limit":false,"percent_used":{"seven_day_overage_included":21,"seven_day":43},"blocking":[],"arm":null,"type":"full","unavailable_reason":"unavailable","gives_back_weekly":true},{"id":"grant_c","label":"New model launch: one more Fable reset at 12 months tenure","resets_total":1,"resets_left":1,"starts_at":"2026-11-09T18:00:00+00:00","ends_at":"2027-03-09T07:59:00+00:00","clears":["seven_day_overage_included"],"paused":false,"usable_now":true,"use_requires_limit":false,"percent_used":{"seven_day_overage_included":21,"seven_day":43},"blocking":[],"arm":null,"type":"full","unavailable_reason":null,"gives_back_weekly":true},{"id":"grant_d","label":"New model launch: one more Fable reset at 6 months tenure","resets_total":1,"resets_left":1,"starts_at":"2026-11-09T18:00:00+00:00","ends_at":"2027-03-09T07:59:00+00:00","clears":["seven_day_overage_included"],"paused":false,"usable_now":true,"use_requires_limit":false,"percent_used":{"seven_day_overage_included":21,"seven_day":43},"blocking":[],"arm":null,"type":"full","unavailable_reason":null,"gives_back_weekly":true}],"next_grant_id":"grant_a","weekly_resets_at":"2026-11-16T03:00:00+00:00","cooldown_until":null,"paused_held":false}}`

func Test_BankedStatus_classifies_kinds_and_matches_live_fixture(t *testing.T) {
	// Given
	status := parseBankStatus([]byte(fixtureBankStatus))
	if status == nil {
		t.Fatal("fixture status did not parse")
	}
	// When
	credits := status.credits(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	// Then
	if credits == nil || credits.AvailableCount != 3 || credits.UsableNow == nil || !*credits.UsableNow {
		t.Fatalf("top-level credits = %+v, want 3 usable", credits)
	}
	if len(credits.Kinds) != 2 {
		t.Fatalf("kinds = %+v, want weekly and fable only", credits.Kinds)
	}
	weekly, fable := credits.Kinds[0], credits.Kinds[1]
	if weekly.ID != resetKindWeekly || weekly.Label != "weekly reset" || weekly.AvailableCount != 1 || !weekly.UsableNow ||
		weekly.GivesBackWeekly || weekly.ExpiresAt == nil || *weekly.ExpiresAt != "2026-12-10T22:00:00Z" ||
		len(weekly.Refills) != 2 || weekly.Refills[0] != "weekly" || weekly.Refills[1] != "weekly_fable" {
		t.Fatalf("weekly kind = %+v", weekly)
	}
	if fable.ID != resetKindFable || fable.Label != "Fable reset" || fable.AvailableCount != 2 || !fable.UsableNow ||
		!fable.GivesBackWeekly || fable.ExpiresAt == nil || *fable.ExpiresAt != "2027-03-09T07:59:00Z" ||
		len(fable.Refills) != 1 || fable.Refills[0] != "weekly_fable" {
		t.Fatalf("fable kind = %+v", fable)
	}
}

func Test_BankedStatus_classifies_grant_kind(t *testing.T) {
	// Given
	cases := []struct {
		name      string
		givesBack bool
		grantType string
		clears    []string
		want      string
	}{
		{"gives back weekly wins", true, "weekly", []string{"seven_day"}, resetKindFable},
		{"explicit session", false, "session", nil, resetKindSession},
		{"explicit weekly", false, "weekly", nil, resetKindWeekly},
		{"explicit full", false, "full", nil, resetKindFull},
		{"five hour only", false, "", []string{"five_hour"}, resetKindSession},
		{"no five hour", false, "", []string{"seven_day"}, resetKindWeekly},
		{"empty clears", false, "", nil, resetKindFull},
		{"five hour plus other", false, "", []string{"five_hour", "seven_day"}, resetKindFull},
		{"fable limit clear", false, "", []string{"seven_day_overage_included"}, resetKindWeekly},
		{"unknown type with five hour", false, "mystery", []string{"five_hour"}, resetKindSession},
		{"unknown type with weekly clear", false, "mystery", []string{"seven_day_oauth_apps"}, resetKindWeekly},
		{"unknown type with unknown clear", false, "mystery", []string{"mystery_limit"}, resetKindFull},
		{"unknown type with mixed clears", false, "mystery", []string{"five_hour", "seven_day"}, resetKindFull},
		{"gives back weekly beats unknown type", true, "mystery", []string{"five_hour"}, resetKindFable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant := bankGrant{GivesBackWeekly: tc.givesBack, Type: tc.grantType, Clears: tc.clears}
			if got := grant.kind(); got != tc.want {
				t.Fatalf("kind = %s, want %s", got, tc.want)
			}
		})
	}
}

func Test_BankedStatus_kind_counts_sum_to_top_level(t *testing.T) {
	// Given
	cases := []string{
		fixtureBankStatus,
		`{"cedar_ember":{"eligible":true,"grants":[]}}`,
		`{"cedar_ember":{"eligible":false,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":2,"paused":false,"usable_now":true,"clears":["five_hour"]}]}}`,
		`{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":1,"resets_left":1,"paused":false,"usable_now":true,"clears":["seven_day_opus"]},{"id":"b","resets_total":4,"resets_left":4,"paused":false,"usable_now":false,"type":"full"}]}}`,
	}
	for _, body := range cases {
		credits := parseBankStatus([]byte(body)).credits(time.Now())
		var sum int64
		for _, kind := range credits.Kinds {
			if kind.AvailableCount <= 0 {
				t.Fatalf("listed kind has no resets: %+v", kind)
			}
			sum += kind.AvailableCount
		}
		if sum != credits.AvailableCount {
			t.Fatalf("kind sum = %d, top-level = %d", sum, credits.AvailableCount)
		}
	}
}

func Test_BankedStatus_unknown_type_keeps_counts_and_derives_kind(t *testing.T) {
	// Given
	body := `{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":1,"resets_left":2,"paused":false,"usable_now":true,"type":"future","clears":["seven_day"]}]}}`
	// When
	status := parseBankStatus([]byte(body))
	// Then
	if status == nil {
		t.Fatal("unknown grant type must not make the reading unknown")
	}
	credits := status.credits(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	if credits == nil || credits.AvailableCount != 2 {
		t.Fatalf("credits = %+v, want count 2", credits)
	}
	if len(credits.Kinds) != 1 || credits.Kinds[0].ID != resetKindWeekly || credits.Kinds[0].AvailableCount != 2 || credits.Kinds[0].UsableNow {
		t.Fatalf("kinds = %+v, want counted weekly that is not usable", credits.Kinds)
	}
	if credits.Kinds[0].Next != nil {
		t.Fatalf("unrecognized grant must not be offered next: %+v", credits.Kinds[0].Next)
	}
}

func Test_BankedStatus_unrecognized_grant_counts_toward_totals(t *testing.T) {
	// Given
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	body := `{"cedar_ember":{"eligible":true,"next_grant_id":"known","grants":[` +
		`{"id":"known","resets_total":1,"resets_left":1,"paused":false,"usable_now":true,"type":"weekly"},` +
		`{"id":"future","resets_total":2,"resets_left":2,"paused":false,"usable_now":true,"type":"future","clears":["five_hour"]}]}}`
	status := parseBankStatus([]byte(body))
	if status == nil {
		t.Fatal("unrecognized grant must not make the reading unknown")
	}
	// When
	credits := status.credits(now)
	// Then
	if credits == nil || credits.AvailableCount != 3 {
		t.Fatalf("credits = %+v, want top-level count 3", credits)
	}
	var sum, sessionCount, weeklyCount int64
	var sessionUsable, sessionNext, weeklyUsable bool
	for _, kind := range credits.Kinds {
		sum += kind.AvailableCount
		switch kind.ID {
		case resetKindSession:
			sessionCount, sessionUsable, sessionNext = kind.AvailableCount, kind.UsableNow, kind.Next != nil
		case resetKindWeekly:
			weeklyCount, weeklyUsable = kind.AvailableCount, kind.UsableNow
		}
	}
	if sum != credits.AvailableCount {
		t.Fatalf("kind sum = %d, top-level = %d", sum, credits.AvailableCount)
	}
	if sessionCount != 2 || sessionUsable || sessionNext {
		t.Fatalf("unrecognized kind = count %d usable %t next %t, want counted, unusable, and not offered", sessionCount, sessionUsable, sessionNext)
	}
	if weeklyCount != 1 || !weeklyUsable {
		t.Fatalf("recognized kind = count %d usable %t, want counted and usable", weeklyCount, weeklyUsable)
	}
}
