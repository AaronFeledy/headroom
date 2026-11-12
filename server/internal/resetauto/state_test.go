package resetauto

import (
	"context"
	"slices"
	"testing"
	"time"
)

func Test_Project_reports_an_armed_window(t *testing.T) {
	// Given
	record := Record{
		ArmedAt:            testNow().Add(-time.Hour),
		WindowEndsAt:       testWindow(),
		Kinds:              []string{"weekly", "fable"},
		AccountFingerprint: testFingerprint,
	}

	// When
	state := Project(record, testNow())

	// Then
	if !state.Armed || state.ArmedAt == nil || state.WindowEndsAt == nil || state.AccountFingerprint == nil {
		t.Fatalf("armed state = %+v", state)
	}
	if *state.ArmedAt != record.ArmedAt.Format(time.RFC3339) || *state.WindowEndsAt != testWindow().Format(time.RFC3339) || *state.AccountFingerprint != testFingerprint {
		t.Fatalf("state times/fingerprint = %+v", state)
	}
	if !slices.Equal(state.Kinds, []string{"weekly", "fable"}) {
		t.Fatalf("state kinds = %+v", state.Kinds)
	}
}

func Test_Project_never_reports_an_ended_window_as_armed(t *testing.T) {
	// Given
	event := newEvent(testNow().Add(-time.Hour), "completed", "reset", "", "weekly")
	record := Record{
		ArmedAt:            testNow().Add(-time.Hour),
		WindowEndsAt:       testNow(),
		Kinds:              []string{"weekly"},
		AccountFingerprint: testFingerprint,
		LastEvent:          event,
	}

	// When
	state := Project(record, testNow())

	// Then
	if state.Armed || state.ArmedAt != nil || state.WindowEndsAt != nil || state.Kinds != nil || state.AccountFingerprint != nil {
		t.Fatalf("ended window reported as armed: %+v", state)
	}
	if state.LastEvent == nil || state.LastEvent.Event != "completed" {
		t.Fatalf("last event = %+v", state.LastEvent)
	}
}

func Test_Project_reports_a_disarmed_record_as_an_object_with_no_fields(t *testing.T) {
	// Given
	record := Record{LastEvent: newEvent(testNow(), "canceled", "", ReasonUser, "")}

	// When
	state := Project(record, testNow())

	// Then
	if state.Armed || state.ArmedAt != nil || state.WindowEndsAt != nil || state.Kinds != nil || state.AccountFingerprint != nil {
		t.Fatalf("disarmed state = %+v", state)
	}
}

func Test_Automation_state_is_null_without_a_directory_and_disarmed_when_damaged(t *testing.T) {
	// Given
	unavailable := New(Options{})
	if state, ok := unavailable.State(ProviderClaude); ok || state != nil {
		t.Fatalf("unavailable state = %+v, ok=%v", state, ok)
	}
	automation := newTestAutomation(t)
	writeStateFile(t, automation.store, "{")

	// When
	state, ok := automation.State(ProviderCodex)

	// Then
	if !ok || state == nil || state.Armed || state.LastEvent != nil {
		t.Fatalf("damaged store state = %+v, ok=%v", state, ok)
	}
}

func Test_Automation_state_does_not_leak_internal_record_fields(t *testing.T) {
	// Given
	automation := newTestAutomation(t)
	record, err := automation.store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil || !record.Armed() {
		t.Fatalf("record = %+v, error=%v", record, err)
	}

	// When
	state, ok := automation.State(ProviderCodex)

	// Then
	if !ok || state == nil || !state.Armed || state.Kinds != nil {
		t.Fatalf("codex state = %+v, ok=%v", state, ok)
	}
	// The API state never carries the provider account ID; only the fingerprint.
	if state.AccountFingerprint == nil || *state.AccountFingerprint != testFingerprint || state.LastEvent != nil {
		t.Fatalf("state content = %+v", state)
	}
}
