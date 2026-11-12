package resetauto

import (
	"math"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testNow() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }

func testWindow() time.Time { return testNow().Add(48 * time.Hour) }

func credits(fingerprint string, count int64, usable *bool, kinds ...usage.ResetCreditKind) *usage.RateLimitResetCredits {
	return &usage.RateLimitResetCredits{AvailableCount: count, AccountFingerprint: &fingerprint, UsableNow: usable, Kinds: kinds}
}

func weeklyBucket(utilization float64, resetsAt time.Time) usage.Bucket {
	return usage.Bucket{ID: usage.BucketWeekly, Label: "Weekly", Utilization: utilization, ResetsAt: &resetsAt}
}

func kind(id string, count int64, usable bool, refills []string, givesBackWeekly bool) usage.ResetCreditKind {
	return usage.ResetCreditKind{ID: id, Label: id, AvailableCount: count, UsableNow: usable, Refills: refills, GivesBackWeekly: givesBackWeekly}
}

func claudeData(kinds []usage.ResetCreditKind, available int64, usable *bool, buckets ...usage.Bucket) usage.UsageData {
	return usage.UsageData{Buckets: buckets, RateLimitResetCredits: credits(testFingerprint, available, usable, kinds...)}
}

func withFetchedAt(entry poller.Entry, at time.Time) poller.Entry {
	entry.FetchedAt = at
	return entry
}

func Test_Decide_codex_rules(t *testing.T) {
	// Given
	window := testWindow()
	armed := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: window, AccountFingerprint: testFingerprint}
	entry := func(utilization float64, available int64, mutate func(*usage.UsageData)) poller.Entry {
		data := usage.UsageData{Buckets: []usage.Bucket{weeklyBucket(utilization, window)}, RateLimitResetCredits: credits(testFingerprint, available, nil)}
		if mutate != nil {
			mutate(&data)
		}
		return poller.Entry{Data: data, FetchedAt: testNow()}
	}
	tests := []struct {
		name  string
		now   time.Time
		entry poller.Entry
		want  Decision
	}{
		{"window ended", window, entry(100, 1, nil), Decision{Action: ActionCancel, Reason: ReasonWindowEnded}},
		{"ended window cancels before freshness", window, withFetchedAt(entry(100, 1, nil), time.Time{}), Decision{Action: ActionCancel, Reason: ReasonWindowEnded}},
		{"error reading keeps", testNow(), entry(100, 1, func(d *usage.UsageData) { message := "failed"; d.Error = &message }), Decision{}},
		{"missing credits keeps", testNow(), entry(100, 1, func(d *usage.UsageData) { d.RateLimitResetCredits = nil }), Decision{}},
		{"unknown fingerprint keeps", testNow(), entry(100, 1, func(d *usage.UsageData) { d.RateLimitResetCredits.AccountFingerprint = nil }), Decision{}},
		{"changed identity cancels", testNow(), entry(100, 1, func(d *usage.UsageData) {
			other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
			d.RateLimitResetCredits.AccountFingerprint = &other
		}), Decision{Action: ActionCancel, Reason: ReasonIdentityChanged}},
		{"missing weekly keeps", testNow(), entry(100, 1, func(d *usage.UsageData) { d.Buckets = nil }), Decision{}},
		{"missing reset time keeps", testNow(), entry(100, 1, func(d *usage.UsageData) { d.Buckets[0].ResetsAt = nil }), Decision{}},
		{"different window cancels", testNow(), entry(100, 1, func(d *usage.UsageData) {
			other := window.Add(time.Hour)
			d.Buckets[0].ResetsAt = &other
		}), Decision{Action: ActionCancel, Reason: ReasonIdentityChanged}},
		{"subsecond window matches", testNow(), entry(100, 1, func(d *usage.UsageData) {
			at := window.Add(500 * time.Millisecond).In(time.FixedZone("offset", 3600))
			d.Buckets[0].ResetsAt = &at
		}), Decision{Action: ActionFire}},
		{"weekly meter after unrelated meter fires", testNow(), entry(100, 1, func(d *usage.UsageData) { d.Buckets = append([]usage.Bucket{{ID: usage.BucketSession}}, d.Buckets...) }), Decision{Action: ActionFire}},
		{"non-finite utilization keeps", testNow(), entry(math.NaN(), 1, nil), Decision{}},
		{"positive infinite utilization keeps", testNow(), entry(math.Inf(1), 1, nil), Decision{}},
		{"negative infinite utilization keeps", testNow(), entry(math.Inf(-1), 1, nil), Decision{}},
		{"below floor cancels", testNow(), entry(89.9, 1, nil), Decision{Action: ActionCancel, Reason: ReasonBelowFloor}},
		{"at floor keeps", testNow(), entry(90, 1, nil), Decision{}},
		{"no credits cancels", testNow(), entry(95, 0, nil), Decision{Action: ActionCancel, Reason: ReasonNoCredits}},
		{"negative credits cancels", testNow(), entry(100, -1, nil), Decision{Action: ActionCancel, Reason: ReasonNoCredits}},
		{"full utilization fires", testNow(), entry(100, 1, nil), Decision{Action: ActionFire}},
		{"above full utilization fires", testNow(), entry(120, 1, nil), Decision{Action: ActionFire}},
		{"near the limit keeps", testNow(), entry(99.9, 1, nil), Decision{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := Decide(ProviderCodex, armed, tt.entry, tt.now)

			// Then
			if got != tt.want {
				t.Fatalf("decision = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func Test_Decide_claude_ignores_copied_session_meter(t *testing.T) {
	// Given a copied weekly window in the session slot and no real session.
	window := testWindow()
	armed := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: window, Kinds: []string{"session"}, AccountFingerprint: testFingerprint}
	buckets := []usage.Bucket{
		{ID: usage.BucketSession, Label: "Weekly", Utilization: 100, ResetsAt: &window},
		weeklyBucket(10, window),
	}
	kinds := []usage.ResetCreditKind{kind("session", 1, true, []string{"session", "weekly"}, false)}
	data := claudeData(kinds, 1, nil, buckets...)
	data.SessionFromFiveHour = false

	// When
	decision := Decide(ProviderClaude, armed, poller.Entry{Data: data, FetchedAt: testNow()}, testNow())

	// Then
	if decision.Action != ActionKeep {
		t.Fatalf("copied session meter fired: %+v", decision)
	}

	// A real five-hour window at full utilization may fire.
	data.SessionFromFiveHour = true
	decision = Decide(ProviderClaude, armed, poller.Entry{Data: data, FetchedAt: testNow()}, testNow())
	if decision.Action != ActionFire || decision.Kind != "session" {
		t.Fatalf("real session wall = %+v, want fire session", decision)
	}
}

func Test_Decide_claude_rules(t *testing.T) {
	// Given
	window := testWindow()
	armed := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: window, Kinds: []string{"weekly", "fable"}, AccountFingerprint: testFingerprint}
	base := func(buckets []usage.Bucket, kinds []usage.ResetCreditKind, available int64) poller.Entry {
		data := claudeData(kinds, available, nil, buckets...)
		data.SessionFromFiveHour = true
		return poller.Entry{Data: data, FetchedAt: testNow()}
	}
	weeklyKinds := []usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}
	tests := []struct {
		name  string
		entry poller.Entry
		want  Decision
	}{
		{"kinds absent keeps", base([]usage.Bucket{weeklyBucket(100, window)}, nil, 1), Decision{}},
		{"empty kinds cancels", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{}, 1), Decision{Action: ActionCancel, Reason: ReasonKindUnavailable}},
		{"unapproved kind cancels", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{kind("full", 1, true, []string{"weekly"}, false)}, 1), Decision{Action: ActionCancel, Reason: ReasonKindUnavailable}},
		{"no allowed available kind cancels", base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("fable", 0, true, []string{"weekly_fable"}, false)}, 1),
			Decision{Action: ActionCancel, Reason: ReasonKindUnavailable}},
		{"kind does not apply cancels", base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("fable", 1, true, []string{"weekly_opus"}, false)}, 1),
			Decision{Action: ActionCancel, Reason: ReasonNoTrigger}},
		{"no wall keeps", base([]usage.Bucket{weeklyBucket(60, window)}, weeklyKinds, 1), Decision{}},
		{"status-only wall keeps", base([]usage.Bucket{
			{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 100, Measurement: usage.MeasurementStatus, ResetsAt: &window},
		}, weeklyKinds, 1), Decision{}},
		{"unknown measurement keeps", base([]usage.Bucket{{ID: usage.BucketWeekly, Utilization: 100, Measurement: "unknown", ResetsAt: &window}}, weeklyKinds, 1), Decision{}},
		{"explicit percent wall fires", base([]usage.Bucket{{ID: usage.BucketWeekly, Utilization: 100, Measurement: usage.MeasurementPercent, ResetsAt: &window}}, weeklyKinds, 1), Decision{Action: ActionFire, Kind: "weekly"}},
		{"nan wall keeps", base([]usage.Bucket{weeklyBucket(math.NaN(), window)}, weeklyKinds, 1), Decision{}},
		{"infinite wall keeps", base([]usage.Bucket{weeklyBucket(math.Inf(1), window)}, weeklyKinds, 1), Decision{}},
		{"above full wall fires", base([]usage.Bucket{weeklyBucket(120, window)}, weeklyKinds, 1), Decision{Action: ActionFire, Kind: "weekly"}},
		{"inapplicable usable kind cannot fire", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{
			kind("fable", 1, true, []string{"weekly_fable"}, false), kind("weekly", 1, false, []string{"weekly"}, false),
		}, 2), Decision{}},
		{"unapproved kind cannot outrank allowed kind", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{
			kind("full", 1, true, []string{"weekly"}, false), kind("weekly", 1, true, []string{"weekly"}, false),
		}, 2), Decision{Action: ActionFire, Kind: "weekly"}},
		{"depleted kind cannot outrank available kind", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{
			kind("fable", 0, true, []string{"weekly"}, false), kind("weekly", 1, true, []string{"weekly"}, false),
		}, 1), Decision{Action: ActionFire, Kind: "weekly"}},
		{"weekly give-back alone fires", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{kind("fable", 1, true, nil, true)}, 1), Decision{Action: ActionFire, Kind: "fable"}},
		{"weekly give-back cannot refill model wall", base([]usage.Bucket{weeklyBucket(10, window), {ID: "weekly_fable", Utilization: 100}}, []usage.ResetCreditKind{kind("fable", 1, true, nil, true)}, 1), Decision{}},
		{"unusable first kind yields to usable kind", base([]usage.Bucket{weeklyBucket(100, window)}, []usage.ResetCreditKind{
			kind("weekly", 1, false, []string{"weekly"}, false), kind("fable", 1, true, []string{"weekly"}, false),
		}, 2), Decision{Action: ActionFire, Kind: "fable"}},
		{"later wall can fire", base([]usage.Bucket{weeklyBucket(100, window), {ID: "weekly_fable", Utilization: 100}}, []usage.ResetCreditKind{
			kind("weekly", 1, false, []string{"weekly"}, false), kind("fable", 1, true, []string{"weekly_fable"}, false),
		}, 2), Decision{Action: ActionFire, Kind: "fable"}},
		{"walls keep reading order", base([]usage.Bucket{{ID: "weekly_fable", Utilization: 100}, weeklyBucket(100, window)}, []usage.ResetCreditKind{
			kind("weekly", 1, true, []string{"weekly"}, false), kind("fable", 1, true, []string{"weekly_fable"}, false),
		}, 2), Decision{Action: ActionFire, Kind: "fable"}},
		{"usable wall fires", base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}, 1),
			Decision{Action: ActionFire, Kind: "weekly"}},
		{"unusable kind keeps", base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("weekly", 1, false, []string{"weekly"}, false)}, 1), Decision{}},
		{"zero fetch time keeps", withFetchedAt(base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}, 1), time.Time{}), Decision{}},
		{"stale reading at a wall keeps", withFetchedAt(base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}, 1), testNow().Add(-3*time.Minute)), Decision{}},
		{"reading at freshness boundary fires", withFetchedAt(base([]usage.Bucket{weeklyBucket(100, window)},
			[]usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}, 1), testNow().Add(-2*time.Minute)), Decision{Action: ActionFire, Kind: "weekly"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := Decide(ProviderClaude, armed, tt.entry, testNow())

			// Then
			if got != tt.want {
				t.Fatalf("decision = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func Test_Decide_claude_prefers_fewer_refills_then_server_order_then_weekly_givers(t *testing.T) {
	// Given
	window := testWindow()
	armed := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: window, Kinds: []string{"weekly", "fable", "session"}, AccountFingerprint: testFingerprint}
	kinds := []usage.ResetCreditKind{
		kind("weekly", 1, true, []string{"weekly", "weekly_fable", "session"}, false),
		kind("fable", 1, true, []string{"weekly_fable"}, true),
		kind("session", 1, true, []string{"session"}, false),
	}
	fableWall := usage.Bucket{ID: "weekly_fable", Label: "Fable", Utilization: 100, ResetsAt: &window}
	data := claudeData(kinds, 3, nil, weeklyBucket(10, window), fableWall)
	data.SessionFromFiveHour = true

	// When fewer refills are preferred at the wall.
	decision := Decide(ProviderClaude, armed, poller.Entry{Data: data, FetchedAt: testNow()}, testNow())

	// Then
	if decision.Action != ActionFire || decision.Kind != "fable" {
		t.Fatalf("preference = %+v, want fire fable", decision)
	}

	// Ties keep the server's kind order.
	tieKinds := []usage.ResetCreditKind{
		kind("session", 1, true, []string{"weekly_fable"}, false),
		kind("fable", 1, true, []string{"weekly_fable"}, false),
	}
	data = claudeData(tieKinds, 2, nil, weeklyBucket(10, window), fableWall)
	decision = Decide(ProviderClaude, armed, poller.Entry{Data: data, FetchedAt: testNow()}, testNow())
	if decision.Action != ActionFire || decision.Kind != "session" {
		t.Fatalf("tie preference = %+v, want fire session", decision)
	}

	// A kind that only gives back the weekly window comes last.
	weeklyWall := usage.Bucket{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 100, ResetsAt: &window}
	givers := []usage.ResetCreditKind{
		kind("fable", 1, true, nil, true),
		kind("weekly", 1, true, []string{"weekly"}, false),
	}
	data = claudeData(givers, 2, nil, weeklyWall)
	decision = Decide(ProviderClaude, armed, poller.Entry{Data: data, FetchedAt: testNow()}, testNow())
	if decision.Action != ActionFire || decision.Kind != "weekly" {
		t.Fatalf("weekly giver preference = %+v, want fire weekly", decision)
	}
}

func Test_Decide_not_armed_keeps(t *testing.T) {
	// Given
	record := Record{WindowEndsAt: testWindow(), AccountFingerprint: testFingerprint}

	// When
	entry := poller.Entry{Data: usage.UsageData{Buckets: []usage.Bucket{weeklyBucket(100, testWindow())}, RateLimitResetCredits: credits(testFingerprint, 1, nil)}, FetchedAt: testNow()}
	decision := Decide(ProviderCodex, record, entry, testNow())

	// Then
	if decision != (Decision{}) {
		t.Fatalf("decision = %+v, want keep", decision)
	}
}
