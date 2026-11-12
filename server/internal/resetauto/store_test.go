package resetauto

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "reset-automation"))
	store.now = testNow
	return store
}

func testArmRequest() ArmRequest {
	return ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow(), Kinds: []string{"weekly", "fable"}}
}

func Test_Store_round_trips_with_private_modes(t *testing.T) {
	// Given
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not reported on Windows")
	}
	store := newTestStore(t)

	// When
	record, err := store.Arm(context.Background(), ProviderClaude, testArmRequest())

	// Then
	if err != nil || !record.Armed() || record.AccountFingerprint != testFingerprint {
		t.Fatalf("armed record = %+v, error=%v", record, err)
	}
	loaded, err := store.Load()
	if err != nil || !loaded[ProviderClaude].Armed() {
		t.Fatalf("loaded = %+v, error=%v", loaded, err)
	}
	dirInfo, err := os.Stat(store.dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory info = %v, error=%v", dirInfo, err)
	}
	fileInfo, err := os.Stat(filepath.Join(store.dir, stateFileName))
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file info = %v, error=%v", fileInfo, err)
	}
}

func Test_Store_rearm_is_idempotent_only_for_the_same_request(t *testing.T) {
	// Given
	store := newTestStore(t)
	first, err := store.Arm(context.Background(), ProviderClaude, testArmRequest())
	if err != nil {
		t.Fatal(err)
	}

	// When an identical request arrives, nothing changes.
	store.now = func() time.Time { return testNow().Add(time.Minute) }
	again, err := store.Arm(context.Background(), ProviderClaude, testArmRequest())

	// Then
	if err != nil || !again.ArmedAt.Equal(first.ArmedAt) {
		t.Fatalf("identical re-arm = %+v, error=%v", again, err)
	}
	different := testArmRequest()
	different.WindowEndsAt = different.WindowEndsAt.Add(time.Hour)
	if _, err := store.Arm(context.Background(), ProviderClaude, different); !errors.Is(err, ErrConflicting) {
		t.Fatalf("different re-arm error = %v, want conflict", err)
	}
}

func Test_Store_fire_transition_disarms_before_recording_the_request(t *testing.T) {
	// Given
	store := newTestStore(t)
	record, err := store.Arm(context.Background(), ProviderClaude, testArmRequest())
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = store.BeginFire(context.Background(), ProviderClaude, record, "fable")

	// Then
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	disarmed := loaded[ProviderClaude]
	if disarmed.Armed() || disarmed.AccountFingerprint != "" || disarmed.Kinds != nil {
		t.Fatalf("authorization survived the fire transition: %+v", disarmed)
	}
	if disarmed.LastEvent == nil || disarmed.LastEvent.Event != "requested" || disarmed.LastEvent.Kind == nil || *disarmed.LastEvent.Kind != "fable" {
		t.Fatalf("requested event = %+v", disarmed.LastEvent)
	}

	// A second transition with the spent authorization must not spend again.
	if err := store.BeginFire(context.Background(), ProviderClaude, record, "fable"); !errors.Is(err, ErrStaleAuthorization) {
		t.Fatalf("replayed BeginFire error = %v, want stale", err)
	}
}

func Test_Store_records_completion_after_the_authorization_is_gone(t *testing.T) {
	// Given
	store := newTestStore(t)
	record, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginFire(context.Background(), ProviderCodex, record, ""); err != nil {
		t.Fatal(err)
	}

	// When
	err = store.FinishFire(context.Background(), ProviderCodex, newEvent(testNow(), "completed", "reset", "", ""))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	event := loaded[ProviderCodex].LastEvent
	if loaded[ProviderCodex].Armed() || event == nil || event.Event != "completed" || event.Outcome == nil || *event.Outcome != "reset" {
		t.Fatalf("stored event = %+v", event)
	}
}

func Test_Store_cancel_is_idempotent_and_keeps_the_last_event(t *testing.T) {
	// Given
	store := newTestStore(t)
	record, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil {
		t.Fatal(err)
	}

	// When
	canceled, err := store.Cancel(context.Background(), ProviderCodex, ReasonUser)

	// Then
	if err != nil || canceled.Armed() || canceled.LastEvent == nil || canceled.LastEvent.Reason == nil || *canceled.LastEvent.Reason != ReasonUser {
		t.Fatalf("canceled = %+v, error=%v", canceled, err)
	}
	store.now = func() time.Time { return testNow().Add(time.Minute) }
	again, err := store.Cancel(context.Background(), ProviderCodex, ReasonUser)
	if err != nil || again.LastEvent == nil || again.LastEvent.Reason == nil || *again.LastEvent.Reason != ReasonUser || again.LastEvent.At != canceled.LastEvent.At {
		t.Fatalf("repeated cancel = %+v, error=%v", again, err)
	}
	if !record.Armed() {
		t.Fatal("cancel mutated the caller's record")
	}
}

func Test_Store_evaluation_cancel_requires_the_observed_authorization(t *testing.T) {
	// Given
	store := newTestStore(t)
	observed, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil {
		t.Fatal(err)
	}
	other := observed
	other.ArmedAt = observed.ArmedAt.Add(time.Second)

	// When
	if _, err := store.CancelIfEvaluation(context.Background(), ProviderCodex, other, ReasonWindowEnded); err != nil {
		t.Fatal(err)
	}

	// Then
	loaded, err := store.Load()
	if err != nil || !loaded[ProviderCodex].Armed() {
		t.Fatalf("a stale evaluation canceled the authorization: %+v, error=%v", loaded[ProviderCodex], err)
	}
	if _, err := store.CancelIfEvaluation(context.Background(), ProviderCodex, observed, ReasonWindowEnded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load()
	if err != nil || loaded[ProviderCodex].Armed() || loaded[ProviderCodex].LastEvent == nil {
		t.Fatalf("evaluation cancel = %+v, error=%v", loaded[ProviderCodex], err)
	}
}

func writeStateFile(t *testing.T, store *Store, content string) {
	t.Helper()
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, stateFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func Test_Store_rejects_damaged_or_unknown_state_without_rewriting_it(t *testing.T) {
	// Given
	tests := []struct {
		name    string
		content string
	}{
		{"unknown version", `{"version":2,"providers":{}}`},
		{"unknown field", `{"version":1,"providers":{},"extra":1}`},
		{"duplicate key", `{"version":1,"providers":{},"version":1}`},
		{"trailing data", `{"version":1,"providers":{}}{}`},
		{"unknown provider", `{"version":1,"providers":{"other":{}}}`},
		{"malformed event", `{"version":1,"providers":{"codex":{"armed_at":"2026-11-11T12:00:00Z","window_ends_at":"2026-11-13T12:00:00Z","kinds":null,"account_fingerprint":"` + testFingerprint + `","last_event":{"at":"yesterday","event":"requested","outcome":null,"reason":null,"kind":null}}}}`},
		{"unknown completed outcome", `{"version":1,"providers":{"codex":{"armed_at":null,"window_ends_at":null,"kinds":null,"account_fingerprint":null,"last_event":{"at":"2026-11-11T12:00:00Z","event":"completed","outcome":"maybe","reason":null,"kind":null}}}}`},
		{"unarmed with fields", `{"version":1,"providers":{"claude":{"armed_at":null,"window_ends_at":"2026-11-13T12:00:00Z","kinds":null,"account_fingerprint":null,"last_event":null}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			writeStateFile(t, store, tt.content)

			// When
			_, err := store.Load()
			_, armErr := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})

			// Then
			if !errors.Is(err, ErrStateUnavailable) || !errors.Is(armErr, ErrStateUnavailable) {
				t.Fatalf("load=%v arm=%v, want unavailable", err, armErr)
			}
			data, readErr := os.ReadFile(filepath.Join(store.dir, stateFileName))
			if readErr != nil || string(data) != tt.content {
				t.Fatalf("damaged state was rewritten: %q, error=%v", data, readErr)
			}
		})
	}
}

func Test_Store_reports_unreadable_state_without_rewriting_it(t *testing.T) {
	// Given
	store := newTestStore(t)
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(store.dir, stateFileName)
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := store.Load()

	// Then
	if !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("load error = %v, want unavailable", err)
	}
	info, statErr := os.Stat(blocked)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("unreadable state was replaced, error=%v", statErr)
	}
}

func Test_Store_without_a_directory_is_unavailable(t *testing.T) {
	// Given
	store := NewStore("relative/reset-automation")

	// When
	_, err := store.Load()

	// Then
	if store.Available() || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("relative store = available %v, error=%v", store.Available(), err)
	}
}

func Test_Store_expired_authorization_does_not_block_a_new_arm(t *testing.T) {
	// Given
	store := newTestStore(t)
	first, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return first.WindowEndsAt.Add(time.Minute) }
	later := first.WindowEndsAt.Add(7 * 24 * time.Hour)

	// When
	second, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: later})

	// Then
	if err != nil || !second.Armed() || !second.WindowEndsAt.Equal(later) {
		t.Fatalf("replacement arm = %+v, error=%v", second, err)
	}
	if err := store.BeginFire(context.Background(), ProviderCodex, first, ""); !errors.Is(err, ErrStaleAuthorization) {
		t.Fatalf("expired authorization fired: %v", err)
	}
}

func Test_Store_manual_reset_cancel_records_only_when_armed(t *testing.T) {
	// Given
	store := newTestStore(t)

	// When nothing is armed, cancel is a no-op that writes no state.
	record, err := store.Cancel(context.Background(), ProviderClaude, ReasonManualReset)

	// Then
	if err != nil || record.Armed() || record.LastEvent != nil {
		t.Fatalf("cancel with nothing armed = %+v, error=%v", record, err)
	}
	if _, statErr := os.Stat(filepath.Join(store.dir, stateFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("manual reset cancel wrote state without an authorization: %v", statErr)
	}

	// Given an armed authorization, the manual reset records the new reason.
	if _, err := store.Arm(context.Background(), ProviderClaude, testArmRequest()); err != nil {
		t.Fatal(err)
	}

	// When
	canceled, err := store.Cancel(context.Background(), ProviderClaude, ReasonManualReset)

	// Then
	if err != nil || canceled.Armed() {
		t.Fatalf("canceled = %+v, error=%v", canceled, err)
	}
	if canceled.LastEvent == nil || canceled.LastEvent.Event != "canceled" || canceled.LastEvent.Reason == nil || *canceled.LastEvent.Reason != ReasonManualReset {
		t.Fatalf("manual reset event = %+v", canceled.LastEvent)
	}
}
