package poller

import (
	"errors"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Poller_keeps_reset_times_within_one_minute(t *testing.T) {
	// Given
	pollStart := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	session := pollStart.Add(time.Hour)
	weekly := pollStart.Add(7 * 24 * time.Hour)
	monthly := pollStart.Add(30 * 24 * time.Hour)
	provider := &sequenceProvider{name: "OpenCodeGo", responses: []providerResult{
		{data: windowReading(strPtr("credential"), session, weekly, monthly)},
		{data: windowReading(strPtr("credential"), session.Add(1500*time.Millisecond), weekly.Add(1500*time.Millisecond), monthly.Add(1500*time.Millisecond))},
		{data: windowReading(strPtr("credential"), session.Add(-2*time.Second), weekly.Add(-2*time.Second), monthly.Add(-2*time.Second))},
	}}
	p := windowPoller(t, pollStart, provider)

	// When / Then
	for range provider.responses {
		assertResets(t, pollProvider(t, p, "OpenCodeGo"), session, weekly, monthly)
	}
}

func Test_Poller_serves_a_larger_reset_change_as_a_new_window(t *testing.T) {
	// Given
	pollStart := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	session := pollStart.Add(2 * time.Minute)
	weekly := pollStart.Add(7 * 24 * time.Hour)
	monthly := pollStart.Add(30 * 24 * time.Hour)
	provider := &sequenceProvider{name: "OpenCodeGo", responses: []providerResult{
		{data: windowReading(strPtr("credential"), pollStart.Add(time.Hour), weekly.Add(-2*time.Minute), monthly.Add(-2*time.Minute))},
		{data: windowReading(strPtr("credential"), session, weekly, monthly)},
	}}
	p := windowPoller(t, pollStart, provider)
	pollProvider(t, p, "OpenCodeGo")

	// When
	entry := pollProvider(t, p, "OpenCodeGo")

	// Then
	assertResets(t, entry, session, weekly, monthly)
}

func Test_Poller_keeps_reset_anchor_through_a_failed_poll(t *testing.T) {
	// Given
	pollStart := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	session := pollStart.Add(time.Hour)
	weekly := pollStart.Add(7 * 24 * time.Hour)
	monthly := pollStart.Add(30 * 24 * time.Hour)
	provider := &sequenceProvider{name: "OpenCodeGo", responses: []providerResult{
		{data: windowReading(strPtr("credential"), session, weekly, monthly)},
		{err: errors.New("dial failed")},
		{data: usage.UsageData{ProviderName: "OpenCodeGo", Error: strPtr("usage unavailable")}},
		{data: windowReading(strPtr("credential"), session.Add(2*time.Second), weekly.Add(2*time.Second), monthly.Add(2*time.Second))},
	}}
	p := windowPoller(t, pollStart, provider)
	for range provider.responses[:3] {
		pollProvider(t, p, "OpenCodeGo")
	}

	// When
	entry := pollProvider(t, p, "OpenCodeGo")

	// Then
	assertResets(t, entry, session, weekly, monthly)
}

func Test_Poller_serves_a_drifting_reset_for_a_new_or_unknown_credential(t *testing.T) {
	// Given
	pollStart := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	session := pollStart.Add(time.Hour)
	weekly := pollStart.Add(7 * 24 * time.Hour)
	monthly := pollStart.Add(30 * 24 * time.Hour)
	provider := &sequenceProvider{name: "OpenCodeGo", responses: []providerResult{
		{data: windowReading(strPtr("credential-one"), session, weekly, monthly)},
		{data: windowReading(strPtr("credential-two"), session.Add(2*time.Second), weekly.Add(2*time.Second), monthly.Add(2*time.Second))},
		{data: windowReading(nil, session.Add(4*time.Second), weekly.Add(4*time.Second), monthly.Add(4*time.Second))},
		{data: windowReading(nil, session.Add(5*time.Second), weekly.Add(5*time.Second), monthly.Add(5*time.Second))},
	}}
	p := windowPoller(t, pollStart, provider)
	pollProvider(t, p, "OpenCodeGo")

	// When
	rotated := pollProvider(t, p, "OpenCodeGo")
	pollProvider(t, p, "OpenCodeGo")
	unknown := pollProvider(t, p, "OpenCodeGo")

	// Then
	assertResets(t, rotated, session.Add(2*time.Second), weekly.Add(2*time.Second), monthly.Add(2*time.Second))
	assertResets(t, unknown, session.Add(5*time.Second), weekly.Add(5*time.Second), monthly.Add(5*time.Second))
}

func windowPoller(t *testing.T, now time.Time, provider *sequenceProvider) *Poller {
	t.Helper()
	p := New(Options{Clock: fixedClock{now: now}})
	if err := p.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	return p
}

func windowReading(epoch *string, session, weekly, monthly time.Time) usage.UsageData {
	data := usage.FromBuckets("OpenCodeGo", []usage.Bucket{
		{ID: usage.BucketSession, Label: "5-Hour", Utilization: 10, ResetsAt: &session, Measurement: usage.MeasurementPercent},
		{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 20, ResetsAt: &weekly, Measurement: usage.MeasurementPercent},
		{ID: usage.BucketMonthly, Label: "Monthly", Utilization: 30, ResetsAt: &monthly, Measurement: usage.MeasurementPercent},
	})
	data.CredentialEpoch = epoch
	return data
}

func assertResets(t *testing.T, entry Entry, session, weekly, monthly time.Time) {
	t.Helper()
	want := map[string]time.Time{
		"current":           session,
		"header weekly":     weekly,
		usage.BucketSession: session,
		usage.BucketWeekly:  weekly,
		usage.BucketMonthly: monthly,
	}
	got := map[string]*time.Time{
		"current":       entry.Data.Current.ResetsAt,
		"header weekly": entry.Data.Weekly.ResetsAt,
	}
	for _, bucket := range entry.Data.Buckets {
		got[bucket.ID] = bucket.ResetsAt
	}
	for name, resetsAt := range want {
		if got[name] == nil || !got[name].Equal(resetsAt) {
			t.Fatalf("%s resets_at = %v, want %v", name, got[name], resetsAt)
		}
	}
}
