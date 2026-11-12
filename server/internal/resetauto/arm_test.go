package resetauto

import (
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_ValidateArm_codex_rules(t *testing.T) {
	// Given
	window := testWindow()
	entry := func(utilization float64, available int64, mutate func(*usage.UsageData)) poller.Entry {
		data := usage.UsageData{Buckets: []usage.Bucket{weeklyBucket(utilization, window)}, RateLimitResetCredits: credits(testFingerprint, available, nil)}
		if mutate != nil {
			mutate(&data)
		}
		return poller.Entry{Data: data}
	}
	request := ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: window}
	tests := []struct {
		name  string
		entry poller.Entry
		want  string
	}{
		{"eligible at floor", entry(usage.CodexManualResetFloor, 1, nil), ""},
		{"below floor", entry(89, 1, nil), ArmRejectFloor},
		{"no credits", entry(95, 0, nil), ArmRejectNoCredits},
		{"no reading", entry(95, 1, func(d *usage.UsageData) { message := "failed"; d.Error = &message }), ArmRejectNoReading},
		{"identity mismatch", entry(95, 1, func(d *usage.UsageData) {
			other := testFingerprint[:63] + "0"
			d.RateLimitResetCredits.AccountFingerprint = &other
		}), ArmRejectIdentity},
		{"unknown fingerprint", entry(95, 1, func(d *usage.UsageData) { d.RateLimitResetCredits.AccountFingerprint = nil }), ArmRejectIdentity},
		{"window mismatch", entry(95, 1, func(d *usage.UsageData) {
			other := window.Add(time.Minute)
			d.Buckets[0].ResetsAt = &other
		}), ArmRejectWindow},
		{"window passed", entry(95, 1, func(d *usage.UsageData) {
			past := testNow().Add(-time.Minute)
			d.Buckets[0].ResetsAt = &past
		}), ArmRejectWindow},
		{"no weekly meter", entry(95, 1, func(d *usage.UsageData) { d.Buckets = nil }), ArmRejectNoWeekly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := ValidateArm(ProviderCodex, tt.entry, request, testNow())

			// Then
			if got != tt.want {
				t.Fatalf("validation = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_ValidateArm_claude_rules(t *testing.T) {
	// Given
	window := testWindow()
	eligibleKind := kind("fable", 1, true, []string{"weekly_fable"}, true)
	notUrgentKind := kind("fable", 1, false, []string{"weekly_fable"}, true)
	baseBuckets := func(session float64) []usage.Bucket {
		now := testNow()
		data := []usage.Bucket{
			{ID: usage.BucketSession, Label: "Current", Utilization: session, ResetsAt: &now},
			weeklyBucket(10, window),
			{ID: "weekly_fable", Label: "Fable", Utilization: 10, ResetsAt: &window},
		}
		return data
	}
	entry := func(session float64, sessionReal bool, kinds []usage.ResetCreditKind, available int64, usable *bool, buckets []usage.Bucket) poller.Entry {
		if buckets == nil {
			buckets = baseBuckets(session)
		}
		data := claudeData(kinds, available, usable, buckets...)
		data.SessionFromFiveHour = sessionReal
		return poller.Entry{Data: data}
	}
	request := ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: window, Kinds: []string{"fable"}}
	tests := []struct {
		name  string
		entry poller.Entry
		want  string
	}{
		{"usable kind arms", entry(0, false, []usage.ResetCreditKind{eligibleKind}, 1, nil, nil), ""},
		{"weekly floor arms", entry(0, true, []usage.ResetCreditKind{eligibleKind}, 1, nil, []usage.Bucket{
			weeklyBucket(90, window), {ID: "weekly_fable", Label: "Fable", Utilization: 10, ResetsAt: &window},
		}), ""},
		{"real session floor arms", entry(95, true, []usage.ResetCreditKind{eligibleKind}, 1, nil, nil), ""},
		{"top-level usable now arms", entry(0, false, []usage.ResetCreditKind{eligibleKind}, 1, boolPtr(true), nil), ""},
		{"copied session floor does not arm", entry(100, false, []usage.ResetCreditKind{notUrgentKind}, 1, nil, nil), ArmRejectNotUrgent},
		{"listed usable kind arms", entry(0, false, []usage.ResetCreditKind{kind("fable", 1, true, []string{"weekly_fable"}, true)}, 1, nil, nil), ""},
		{"kinds required", entry(0, false, []usage.ResetCreditKind{eligibleKind}, 1, nil, nil), ""},
		{"kind unavailable", entry(0, false, []usage.ResetCreditKind{kind("fable", 0, true, []string{"weekly_fable"}, true)}, 1, nil, nil), ArmRejectKindUnavailable},
		{"kind not listed", entry(0, false, []usage.ResetCreditKind{kind("weekly", 1, true, []string{"weekly"}, false)}, 1, nil, nil), ArmRejectKindUnavailable},
		{"kind does not apply", entry(0, false, []usage.ResetCreditKind{kind("fable", 1, true, []string{"weekly_opus"}, false)}, 1, nil, nil), ArmRejectNoTrigger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := request
			if tt.name == "kinds required" {
				request.Kinds = nil
				tt.want = ArmRejectKindsRequired
			}
			// When
			got := ValidateArm(ProviderClaude, tt.entry, request, testNow())

			// Then
			if got != tt.want {
				t.Fatalf("validation = %q, want %q", got, tt.want)
			}
		})
	}
}

func boolPtr(value bool) *bool { return &value }
