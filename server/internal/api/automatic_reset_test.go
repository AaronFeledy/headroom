package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/resetauto"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const automaticFixtureFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func decodeAutomaticFixture(t *testing.T, body string, allowKinds bool) (automaticResetRequest, error) {
	t.Helper()
	return decodeAutomaticResetRequest(json.NewDecoder(strings.NewReader(body)), allowKinds)
}

func Test_decodeAutomaticResetRequest_accepts_each_provider_shape(t *testing.T) {
	// Given
	claudeBody := `{"confirmed":true,"account_fingerprint":"` + automaticFixtureFingerprint + `","window_ends_at":"2026-11-13T12:00:00Z","kinds":["weekly","fable"]}`
	codexBody := `{"confirmed":true,"account_fingerprint":"` + automaticFixtureFingerprint + `","window_ends_at":"2026-11-13T12:00:00Z"}`

	// When
	claude, claudeErr := decodeAutomaticFixture(t, claudeBody, true)
	codex, codexErr := decodeAutomaticFixture(t, codexBody, false)

	// Then
	window := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if claudeErr != nil || !claude.Confirmed || claude.AccountFingerprint != automaticFixtureFingerprint {
		t.Fatalf("claude request = %+v, error=%v", claude, claudeErr)
	}
	if !claude.WindowEndsAt.Equal(window) || !slices.Equal(claude.Kinds, []string{"weekly", "fable"}) {
		t.Fatalf("claude window = %v, kinds=%v", claude.WindowEndsAt, claude.Kinds)
	}
	if codexErr != nil || !codex.Confirmed || codex.AccountFingerprint != automaticFixtureFingerprint {
		t.Fatalf("codex request = %+v, error=%v", codex, codexErr)
	}
	if !codex.WindowEndsAt.Equal(window) || codex.Kinds != nil {
		t.Fatalf("codex window = %v, kinds=%v", codex.WindowEndsAt, codex.Kinds)
	}
}

func Test_decodeAutomaticResetRequest_rejects_malformed_bodies(t *testing.T) {
	// Given
	base := `"confirmed":true,"account_fingerprint":"` + automaticFixtureFingerprint + `","window_ends_at":"2026-11-13T12:00:00Z"`
	tests := []struct {
		name       string
		body       string
		allowKinds bool
	}{
		{"unknown key", `{` + base + `,"extra":1,"kinds":["weekly"]}`, true},
		{"duplicate key", `{` + base + `,"kinds":["weekly"],"kinds":["fable"]}`, true},
		{"trailing data", `{` + base + `,"kinds":["weekly"]}{}`, true},
		{"unconfirmed", `{"confirmed":false,"account_fingerprint":"` + automaticFixtureFingerprint + `","window_ends_at":"2026-11-13T12:00:00Z","kinds":["weekly"]}`, true},
		{"bad fingerprint", `{"confirmed":true,"account_fingerprint":"ABC","window_ends_at":"2026-11-13T12:00:00Z","kinds":["weekly"]}`, true},
		{"bad time", `{"confirmed":true,"account_fingerprint":"` + automaticFixtureFingerprint + `","window_ends_at":"tomorrow","kinds":["weekly"]}`, true},
		{"missing kinds", `{` + base + `}`, true},
		{"empty kinds", `{` + base + `,"kinds":[]}`, true},
		{"duplicate kinds", `{` + base + `,"kinds":["weekly","weekly"]}`, true},
		{"uppercase kind", `{` + base + `,"kinds":["Weekly"]}`, true},
		{"too many kinds", `{` + base + `,"kinds":["a","b","c","d","e","f","g","h","i"]}`, true},
		{"codex kinds", `{` + base + `,"kinds":["weekly"]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := decodeAutomaticFixture(t, tt.body, tt.allowKinds)

			// Then
			if err == nil {
				t.Fatal("decoder accepted a malformed body")
			}
		})
	}
}

func Test_resetAutomationProvider_only_default_rows_carry_state(t *testing.T) {
	// Given
	rows := []usage.UsageData{
		{ProviderName: "Claude"},
		{ProviderName: "Claude (Work)", Account: &usage.Account{ID: "work"}},
		{ProviderName: "Codex"},
		{ProviderName: "Cursor"},
		{ProviderName: "Grok"},
		{ProviderName: "OpenCodeGo"},
	}

	// When and Then
	want := []string{"claude", "", "codex", "", "", ""}
	for index, row := range rows {
		if got := resetAutomationProvider(row); got != want[index] {
			t.Fatalf("%s provider = %q, want %q", row.ProviderName, got, want[index])
		}
	}
}

func Test_attachResetAutomation_adds_state_only_for_supported_rows(t *testing.T) {
	// Given
	window := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	automation := resetauto.New(resetauto.Options{Dir: filepath.Join(t.TempDir(), "reset-automation")})
	fingerprint := automaticFixtureFingerprint
	entry := poller.Entry{Data: usage.UsageData{
		ProviderName: "Codex",
		Buckets:      []usage.Bucket{{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 95, ResetsAt: &window}},
		RateLimitResetCredits: &usage.RateLimitResetCredits{
			AvailableCount:     1,
			AccountFingerprint: &fingerprint,
		},
	}}
	if _, err := automation.Arm(context.Background(), resetauto.ProviderCodex, resetauto.ArmRequest{
		AccountFingerprint: automaticFixtureFingerprint,
		WindowEndsAt:       window,
	}, entry); err != nil {
		t.Fatal(err)
	}
	h := &handler{automation: automation}
	data := []usage.UsageData{{ProviderName: "Codex"}, {ProviderName: "Cursor"}}

	// When
	h.attachResetAutomation(data)

	// Then
	if data[0].ResetAutomation == nil || !data[0].ResetAutomation.Armed {
		t.Fatalf("codex state = %+v", data[0].ResetAutomation)
	}
	if data[1].ResetAutomation != nil {
		t.Fatalf("cursor state = %+v", data[1].ResetAutomation)
	}
	encoded, err := json.Marshal(data[1])
	if err != nil || !strings.Contains(string(encoded), `"reset_automation":null`) {
		t.Fatalf("cursor JSON = %s, error=%v", encoded, err)
	}
	encoded, err = json.Marshal(data[0])
	if err != nil || !strings.Contains(string(encoded), `"reset_automation":{"armed":true`) {
		t.Fatalf("codex JSON = %s, error=%v", encoded, err)
	}
}
