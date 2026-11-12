package claude

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func testGrant(id, kind string, left int64, usable bool) bankGrant {
	total := left
	paused := false
	return bankGrant{ID: id, ResetsTotal: &total, ResetsLeft: &left, Paused: &paused, UsableNow: &usable, Type: kind}
}

func int64Pointer(value int64) *int64 { return &value }
func boolPointer(value bool) *bool    { return &value }

func Test_BankedStatus_selects_grant_by_kind_and_time(t *testing.T) {
	// Given
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	future := "2027-01-01T00:00:00Z"
	past := "2026-01-01T00:00:00Z"
	bad := "not-a-time"
	build := func(grants ...bankGrant) *bankStatus {
		return &bankStatus{eligible: true, nextGrantID: grants[0].ID, grants: grants}
	}
	weeklyA := testGrant("weekly_a", resetKindWeekly, 1, true)
	weeklyB := testGrant("weekly_b", resetKindWeekly, 2, true)
	weeklyB.EndsAt = &future
	weeklyB.StartsAt = &past
	cases := []struct {
		name    string
		status  *bankStatus
		kind    string
		grantID string
		wantErr error
	}{
		{"next candidate wins", build(weeklyA, weeklyB), resetKindWeekly, "weekly_a", nil},
		{"first candidate without next", func() *bankStatus { s := build(weeklyA, weeklyB); s.nextGrantID = "missing"; return s }(), resetKindWeekly, "weekly_a", nil},
		{"expired disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].EndsAt = &past; return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"unparsable end disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].EndsAt = &bad; return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"future start disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].StartsAt = &future; return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"unparsable start disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].StartsAt = &bad; return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"paused disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].Paused = boolPointer(true); return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"exhausted disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].ResetsLeft = int64Pointer(0); return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"unusable disqualifies", func() *bankStatus { s := build(weeklyA); s.grants[0].UsableNow = boolPointer(false); return s }(), resetKindWeekly, "", errResetKindUnavailable},
		{"other kind disqualifies", build(weeklyA), resetKindFable, "", errResetKindUnavailable},
		{"legacy single kind", build(weeklyA), "", "weekly_a", nil},
		{"legacy multiple kinds", build(weeklyA, testGrant("fable_a", resetKindFable, 1, true)), "", "", errResetKindAmbiguous},
		{"nil status", nil, resetKindWeekly, "", errResetUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant, err := selectResetGrant(tc.status, tc.kind, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && (grant == nil || grant.ID != tc.grantID) {
				t.Fatalf("grant = %+v, want %s", grant, tc.grantID)
			}
		})
	}
}

func Test_BankedStatus_unrecognized_grant_is_never_selected(t *testing.T) {
	// Given
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	unrecognized := testGrant("future", "", 1, true)
	unrecognized.Type = "future"
	unrecognized.Clears = []string{"five_hour"}
	status := &bankStatus{eligible: true, nextGrantID: "future", grants: []bankGrant{unrecognized}}
	// When / Then
	if grant, err := selectResetGrant(status, resetKindSession, now); !errors.Is(err, errResetKindUnavailable) || grant != nil {
		t.Fatalf("kind selection = %+v, %v, want refusal", grant, err)
	}
	if grant, err := selectResetGrant(status, "", now); !errors.Is(err, errResetUnavailable) || grant != nil {
		t.Fatalf("legacy selection = %+v, %v, want refusal", grant, err)
	}
}

func Test_BankedStatus_next_describes_the_grant_each_kind_would_spend(t *testing.T) {
	// Given
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	endsAt := "2026-12-10T22:00:00+00:00"
	first := testGrant("first", resetKindWeekly, 1, true)
	preferred := testGrant("preferred", resetKindWeekly, 1, true)
	preferred.Label = "  One full limit reset\n(November 2026)  "
	preferred.EndsAt = &endsAt
	blocked := testGrant("blocked", resetKindFable, 1, false)
	blocked.GivesBackWeekly = true
	status := &bankStatus{eligible: true, nextGrantID: "preferred", grants: []bankGrant{first, preferred, blocked}}
	// When
	credits := status.credits(now)
	// Then
	weekly := kindByID(t, credits, resetKindWeekly)
	if weekly.Next == nil || weekly.Next.Label != "One full limit reset (November 2026)" || weekly.Next.ExpiresAt == nil || *weekly.Next.ExpiresAt != "2026-12-10T22:00:00Z" {
		t.Fatalf("weekly next = %+v, want sanitized preferred grant", weekly.Next)
	}
	if blocked := kindByID(t, credits, resetKindFable); blocked.Next != nil {
		t.Fatalf("unusable kind next = %+v, want nil", blocked.Next)
	}
	// Given the preferred grant is no longer a candidate.
	status.nextGrantID = "missing"
	// When
	credits = status.credits(now)
	// Then
	weekly = kindByID(t, credits, resetKindWeekly)
	if weekly.Next == nil || weekly.Next.Label != "" || weekly.Next.ExpiresAt != nil {
		t.Fatalf("weekly next = %+v, want the first candidate", weekly.Next)
	}
}

func kindByID(t *testing.T, credits *usage.RateLimitResetCredits, id string) usage.ResetCreditKind {
	t.Helper()
	for _, kind := range credits.Kinds {
		if kind.ID == id {
			return kind
		}
	}
	t.Fatalf("kind %q missing from %+v", id, credits.Kinds)
	return usage.ResetCreditKind{}
}

func Test_BankedStatus_grant_label_replaces_controls_trims_and_caps(t *testing.T) {
	// Given
	cases := []struct{ name, label, want string }{
		{"controls become spaces", " One\tfull\nreset ", "One full reset"},
		{"whitespace only", " \x00\n ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When
			got := grantLabel(tc.label)
			// Then
			if got != tc.want {
				t.Fatalf("grantLabel(%q) = %q, want %q", tc.label, got, tc.want)
			}
		})
	}
	// When
	long := grantLabel(strings.Repeat("a", nextGrantLabelLimit+40))
	// Then
	if len([]rune(long)) != nextGrantLabelLimit {
		t.Fatalf("long label length = %d, want %d", len([]rune(long)), nextGrantLabelLimit)
	}
}
