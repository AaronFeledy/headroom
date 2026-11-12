package resetauto

import (
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// The reset core polls again before it spends. These rows pin that the reading
// it spends on must itself decide the same spend; nothing here reaches a spend.
func Test_SpendConfirmation_accepts_only_a_reading_that_decides_the_same_spend(t *testing.T) {
	// Given
	fable := kind("fable", 2, true, []string{"weekly_fable"}, true)
	weekly := kind("weekly", 1, true, []string{"weekly", "weekly_fable"}, false)
	fableMeter := func(utilization float64) usage.Bucket {
		return usage.Bucket{ID: "weekly_fable", Label: "Fable", Utilization: utilization}
	}
	claudeRecord := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: testWindow(), Kinds: []string{"weekly", "fable"}, AccountFingerprint: testFingerprint}
	codexRecord := Record{ArmedAt: testNow().Add(-time.Hour), WindowEndsAt: testWindow(), AccountFingerprint: testFingerprint}
	claudeEntry := func(weeklyUsed, fableUsed float64, fetchedAt time.Time) poller.Entry {
		data := claudeData([]usage.ResetCreditKind{weekly, fable}, 3, nil, weeklyBucket(weeklyUsed, testWindow()), fableMeter(fableUsed))
		return poller.Entry{Data: data, FetchedAt: fetchedAt}
	}
	codexEntry := func(weeklyUsed float64) poller.Entry {
		data := usage.UsageData{Buckets: []usage.Bucket{weeklyBucket(weeklyUsed, testWindow())}, RateLimitResetCredits: credits(testFingerprint, 1, nil)}
		return poller.Entry{Data: data, FetchedAt: testNow()}
	}

	for _, tc := range []struct {
		name     string
		provider string
		record   Record
		kind     string
		entry    poller.Entry
		want     bool
	}{
		{"claude wall for the decided kind", ProviderClaude, claudeRecord, "fable", claudeEntry(40, 100, testNow()), true},
		{"claude meter fell below the wall", ProviderClaude, claudeRecord, "fable", claudeEntry(40, 99, testNow()), false},
		{"claude wall now decides another kind", ProviderClaude, claudeRecord, "fable", claudeEntry(100, 40, testNow()), false},
		{"claude reading is stale", ProviderClaude, claudeRecord, "fable", claudeEntry(40, 100, testNow().Add(-10*time.Minute)), false},
		{"codex at the wall", ProviderCodex, codexRecord, "", codexEntry(100), true},
		{"codex between the manual floor and the wall", ProviderCodex, codexRecord, "", codexEntry(95), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When
			got := spendConfirmation(tc.provider, tc.record, tc.kind, testNow)(tc.entry)

			// Then
			if got != tc.want {
				t.Fatalf("confirmation = %t, want %t", got, tc.want)
			}
		})
	}
}
