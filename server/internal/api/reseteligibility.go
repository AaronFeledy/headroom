package api

import (
	"math"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func eligibleForCodexReset(entry poller.Entry) bool {
	if entry.Data.Error != nil || strings.TrimSpace(entry.Data.ProviderAccountID) == "" || entry.Data.RateLimitResetCredits == nil || entry.Data.RateLimitResetCredits.AvailableCount <= 0 {
		return false
	}
	for _, bucket := range entry.Data.Buckets {
		if bucket.ID == usage.BucketWeekly {
			return !math.IsNaN(bucket.Utilization) && !math.IsInf(bucket.Utilization, 0) && bucket.Utilization >= usage.CodexManualResetFloor
		}
	}
	return false
}

const multiKindResetMessage = "This account has more than one kind of banked reset. Update Headroom to choose which one to use."

func eligibleForClaudeReset(entry poller.Entry) bool {
	credits := entry.Data.RateLimitResetCredits
	return entry.Data.Error == nil && entry.Data.ProviderAccountID != "" && credits != nil && credits.AvailableCount > 0 && credits.UsableNow != nil && *credits.UsableNow
}

// eligibleForClaudeResetKind requires fresh, account-bound usage whose requested
// kind is listed with resets available and usable now.
func eligibleForClaudeResetKind(entry poller.Entry, kind string) bool {
	credits := entry.Data.RateLimitResetCredits
	if entry.Data.Error != nil || entry.Data.ProviderAccountID == "" || credits == nil {
		return false
	}
	selected := credits.Kind(kind)
	return selected != nil && selected.AvailableCount > 0 && selected.UsableNow
}

func claudeResetKindAmbiguous(entry poller.Entry) bool {
	return entry.Data.RateLimitResetCredits != nil && entry.Data.RateLimitResetCredits.AvailableKindCount() > 1
}
