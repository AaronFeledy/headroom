package poller

import (
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_copyUsageData_deep_copies_buckets_and_reset_times(t *testing.T) {
	// Given
	reset := time.Date(2026, 7, 12, 22, 30, 0, 0, time.UTC)
	wantReset := reset
	fingerprint := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantFingerprint := fingerprint
	usable := true
	expires := "2026-12-10T22:00:00Z"
	wantExpires := expires
	source := usage.UsageData{
		Buckets: []usage.Bucket{{ID: "weekly", Label: "Weekly", Utilization: 71, ResetsAt: &reset}},
		RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 3, AccountFingerprint: &fingerprint, UsableNow: &usable, Kinds: []usage.ResetCreditKind{
			{ID: "weekly", Label: "weekly reset", AvailableCount: 1, UsableNow: true, Refills: []string{"weekly", "weekly_fable"}, ExpiresAt: &expires},
		}},
	}

	// When
	copied := copyUsageData(source)
	source.Buckets[0].Label = "changed"
	*source.Buckets[0].ResetsAt = reset.Add(time.Hour)
	source.RateLimitResetCredits.AvailableCount = 1
	*source.RateLimitResetCredits.AccountFingerprint = "changed"
	*source.RateLimitResetCredits.UsableNow = false
	source.RateLimitResetCredits.Kinds[0].AvailableCount = 99
	source.RateLimitResetCredits.Kinds[0].Refills[0] = "changed"
	*source.RateLimitResetCredits.Kinds[0].ExpiresAt = "changed"

	// Then
	if len(copied.Buckets) != 1 {
		t.Fatalf("len(Buckets) = %d, want 1", len(copied.Buckets))
	}
	if copied.Buckets[0].Label != "Weekly" || !copied.Buckets[0].ResetsAt.Equal(wantReset) {
		t.Fatalf("copied bucket = %+v, want independent Weekly bucket resetting at %s", copied.Buckets[0], wantReset)
	}
	if copied.RateLimitResetCredits == nil || copied.RateLimitResetCredits.AvailableCount != 3 ||
		copied.RateLimitResetCredits.AccountFingerprint == nil || *copied.RateLimitResetCredits.AccountFingerprint != wantFingerprint || copied.RateLimitResetCredits.UsableNow == nil || !*copied.RateLimitResetCredits.UsableNow {
		t.Fatalf("copied reset credits = %+v, want independent count and fingerprint", copied.RateLimitResetCredits)
	}
	kinds := copied.RateLimitResetCredits.Kinds
	if len(kinds) != 1 || kinds[0].AvailableCount != 1 || kinds[0].ExpiresAt == nil || *kinds[0].ExpiresAt != wantExpires ||
		len(kinds[0].Refills) != 2 || kinds[0].Refills[0] != "weekly" || kinds[0].Refills[1] != "weekly_fable" {
		t.Fatalf("copied kinds = %+v, want independent kind data", kinds)
	}
}
