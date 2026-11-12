package resetauto

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func newTestAutomation(t *testing.T) *Automation {
	t.Helper()
	return New(Options{
		Dir:      filepath.Join(t.TempDir(), "reset-automation"),
		Lifetime: context.Background(),
		Now:      testNow,
	})
}

func armCodex(t *testing.T, automation *Automation, utilization float64, available int64) {
	t.Helper()
	entry := poller.Entry{Data: usage.UsageData{
		Buckets:               []usage.Bucket{weeklyBucket(utilization, testWindow())},
		RateLimitResetCredits: credits(testFingerprint, available, nil),
	}}
	if _, err := automation.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()}, entry); err != nil {
		t.Fatal(err)
	}
}

func Test_Automation_cancels_a_changed_identity_after_a_poll(t *testing.T) {
	// Given
	automation := newTestAutomation(t)
	armCodex(t, automation, 95, 1)
	other := testFingerprint[:63] + "0"
	entry := poller.Entry{Data: usage.UsageData{
		Buckets:               []usage.Bucket{weeklyBucket(100, testWindow())},
		RateLimitResetCredits: credits(other, 1, nil),
	}}

	// When
	automation.Observe("Codex", entry)

	// Then
	records, err := automation.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := records[ProviderCodex]
	if record.Armed() || record.LastEvent == nil || record.LastEvent.Reason == nil || *record.LastEvent.Reason != ReasonIdentityChanged {
		t.Fatalf("record after identity change = %+v", record)
	}
}

func Test_Automation_cancels_when_the_window_has_ended(t *testing.T) {
	// Given
	automation := newTestAutomation(t)
	armCodex(t, automation, 95, 1)

	// When
	automation.Observe("Codex", poller.Entry{})

	// A reading with no credits keeps the authorization, so move the clock past
	// the window and evaluate again.
	automation.now = func() time.Time { return testWindow() }
	automation.Observe("Codex", poller.Entry{})

	// Then
	records, err := automation.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := records[ProviderCodex]
	if record.Armed() || record.LastEvent == nil || *record.LastEvent.Reason != ReasonWindowEnded {
		t.Fatalf("record after window end = %+v", record)
	}
}

func Test_Automation_keeps_a_fire_decision_without_an_executor(t *testing.T) {
	// Given
	automation := newTestAutomation(t)
	armCodex(t, automation, 95, 1)
	entry := poller.Entry{Data: usage.UsageData{
		Buckets:               []usage.Bucket{weeklyBucket(100, testWindow())},
		RateLimitResetCredits: credits(testFingerprint, 1, nil),
	}}

	// When
	automation.Observe("Codex", entry)

	// Then
	records, err := automation.store.Load()
	if err != nil || !records[ProviderCodex].Armed() {
		t.Fatalf("fire decision without an executor disarmed: %+v, error=%v", records[ProviderCodex], err)
	}
}

func Test_Automation_ignores_the_copied_session_meter_during_evaluation(t *testing.T) {
	// Given
	automation := newTestAutomation(t)
	window := testWindow()
	kinds := []usage.ResetCreditKind{
		kind("session", 1, true, []string{"session"}, false),
		kind("weekly", 1, false, []string{"weekly"}, false),
	}
	armEntry := poller.Entry{Data: claudeData(kinds, 1, nil, weeklyBucket(10, window))}
	armEntry.Data.SessionFromFiveHour = false
	if _, err := automation.Arm(context.Background(), ProviderClaude, ArmRequest{
		AccountFingerprint: testFingerprint,
		WindowEndsAt:       window,
		Kinds:              []string{"session", "weekly"},
	}, armEntry); err != nil {
		t.Fatal(err)
	}
	// The next poll copies the weekly window into the session slot at 100%.
	entry := poller.Entry{Data: claudeData(kinds, 1, nil,
		usage.Bucket{ID: usage.BucketSession, Label: "Weekly", Utilization: 100, ResetsAt: &window},
		weeklyBucket(10, window),
	)}
	entry.Data.SessionFromFiveHour = false

	// When
	automation.Observe("Claude", entry)

	// Then the copied session wall must not fire or cancel.
	records, err := automation.store.Load()
	if err != nil || !records[ProviderClaude].Armed() {
		t.Fatalf("copied session meter changed the authorization: %+v, error=%v", records[ProviderClaude], err)
	}
}
