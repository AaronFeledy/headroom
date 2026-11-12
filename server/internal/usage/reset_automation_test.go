package usage_test

import (
	"encoding/json"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_UsageData_always_carries_a_nullable_automatic_reset_key(t *testing.T) {
	// Given
	data := usage.UsageData{ProviderName: "Cursor"}

	// When
	encoded, err := json.Marshal(data)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	assertJSONField(t, encoded, "reset_automation", `null`)
}

func Test_ResetAutomationState_serializes_every_contract_field(t *testing.T) {
	// Given
	armedAt := "2026-11-11T12:00:00Z"
	window := "2026-11-13T12:00:00Z"
	fingerprint := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	kind := "weekly"
	state := usage.ResetAutomationState{
		Armed:              true,
		ArmedAt:            &armedAt,
		WindowEndsAt:       &window,
		Kinds:              []string{"weekly", "fable"},
		AccountFingerprint: &fingerprint,
		LastEvent:          &usage.ResetAutomationEvent{At: armedAt, Event: "requested", Kind: &kind},
	}

	// When
	encoded, err := json.Marshal(state)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	assertJSONField(t, encoded, "armed", `true`)
	assertJSONField(t, encoded, "armed_at", `"2026-11-11T12:00:00Z"`)
	assertJSONField(t, encoded, "window_ends_at", `"2026-11-13T12:00:00Z"`)
	assertJSONField(t, encoded, "kinds", `["weekly","fable"]`)
	assertJSONField(t, encoded, "account_fingerprint", `"`+fingerprint+`"`)
	assertJSONField(t, encoded, "last_event.at", `"2026-11-11T12:00:00Z"`)
	assertJSONField(t, encoded, "last_event.event", `"requested"`)
	assertJSONField(t, encoded, "last_event.outcome", `null`)
	assertJSONField(t, encoded, "last_event.reason", `null`)
	assertJSONField(t, encoded, "last_event.kind", `"weekly"`)
}

func Test_ResetAutomationState_serializes_disarmed_nulls(t *testing.T) {
	// Given
	state := usage.ResetAutomationState{}

	// When
	encoded, err := json.Marshal(state)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"armed":               `false`,
		"armed_at":            `null`,
		"window_ends_at":      `null`,
		"kinds":               `null`,
		"account_fingerprint": `null`,
		"last_event":          `null`,
	} {
		assertJSONField(t, encoded, path, want)
	}
}
