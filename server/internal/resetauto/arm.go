package resetauto

import (
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// ArmRequest is a validated client request to arm one provider.
type ArmRequest struct {
	AccountFingerprint string
	WindowEndsAt       time.Time
	Kinds              []string
}

// Validation codes explain a refused arm. They are stable identifiers, not
// user-facing copy.
const (
	ArmRejectNoReading       = "no_reading"
	ArmRejectIdentity        = "identity_mismatch"
	ArmRejectNoCredits       = "no_credits"
	ArmRejectNoWeekly        = "no_weekly"
	ArmRejectWindow          = "window_mismatch"
	ArmRejectKindsRequired   = "kinds_required"
	ArmRejectFloor           = "below_floor"
	ArmRejectKindUnavailable = "kind_unavailable"
	ArmRejectNoTrigger       = "no_trigger"
	ArmRejectNotUrgent       = "not_urgent"
)

// ValidateArm checks an arm request against a cached reading. It never contacts
// the provider and never spends. The empty string means the request is eligible.
func ValidateArm(provider string, entry poller.Entry, request ArmRequest, now time.Time) string {
	data := entry.Data
	credits := data.RateLimitResetCredits
	if data.Error != nil || credits == nil {
		return ArmRejectNoReading
	}
	if credits.AccountFingerprint == nil || *credits.AccountFingerprint != request.AccountFingerprint {
		return ArmRejectIdentity
	}
	if credits.AvailableCount <= 0 {
		return ArmRejectNoCredits
	}
	weekly, ok := realMeter(data, usage.BucketWeekly)
	if !ok || weekly.ResetsAt == nil {
		return ArmRejectNoWeekly
	}
	if !weekly.ResetsAt.After(now) || !sameInstant(*weekly.ResetsAt, request.WindowEndsAt) {
		return ArmRejectWindow
	}
	if provider == ProviderCodex {
		if !finite(weekly.Utilization) || weekly.Utilization < usage.CodexManualResetFloor {
			return ArmRejectFloor
		}
		return ""
	}
	if len(request.Kinds) == 0 {
		return ArmRejectKindsRequired
	}
	allowed := make([]usage.ResetCreditKind, 0, len(request.Kinds))
	for _, id := range request.Kinds {
		kind := credits.Kind(id)
		if kind == nil || kind.AvailableCount <= 0 {
			return ArmRejectKindUnavailable
		}
		allowed = append(allowed, *kind)
	}
	meters := realMeters(data)
	applies := false
	for _, kind := range allowed {
		if kindAppliesToAny(kind, meters) {
			applies = true
			break
		}
	}
	if !applies {
		return ArmRejectNoTrigger
	}
	if armUrgent(data, credits) {
		return ""
	}
	return ArmRejectNotUrgent
}

func armUrgent(data usage.UsageData, credits *usage.RateLimitResetCredits) bool {
	if session, ok := realMeter(data, usage.BucketSession); ok && finite(session.Utilization) && session.Utilization >= ClaudeArmFloor {
		return true
	}
	if weekly, ok := realMeter(data, usage.BucketWeekly); ok && finite(weekly.Utilization) && weekly.Utilization >= ClaudeArmFloor {
		return true
	}
	if credits.UsableNow != nil && *credits.UsableNow {
		return true
	}
	for _, kind := range credits.Kinds {
		if kind.AvailableCount > 0 && kind.UsableNow {
			return true
		}
	}
	return false
}
