package poller

import (
	"net/http"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// cleanStreakToStepDown is the number of consecutive successful usage
// responses that earns one spacing reduction.
const cleanStreakToStepDown = 4

func providerPolicy(provider usage.Provider) usage.PollPolicy {
	policied, ok := provider.(usage.PollPolicyProvider)
	if !ok {
		return usage.PollPolicy{}
	}
	policy := policied.PollPolicy()
	if policy.MinSpacing <= 0 {
		return usage.PollPolicy{}
	}
	if policy.MaxSpacing < policy.MinSpacing {
		policy.MaxSpacing = policy.MinSpacing
	}
	if policy.IdleSpacing < 0 {
		policy.IdleSpacing = 0
	}
	return policy
}

func (s *providerState) hasPolicy() bool { return s.policy.MinSpacing > 0 }

// scheduledDelay is the configured interval raised to the learned spacing and
// idle delay.
func (s *providerState) scheduledDelay() time.Duration {
	return max(s.interval, s.spacing, s.idle)
}

// insideSpacingWindow reports whether the last attempt that received an HTTP
// response is still younger than the provider's learned spacing.
func (s *providerState) insideSpacingWindow(now time.Time) bool {
	return s.spacing > 0 && !s.responseAt.IsZero() && now.Before(s.responseAt.Add(s.spacing))
}

// learnSpacing adapts the next spacing after an attempt: a usage 429 doubles
// it, and four clean usage responses step it back toward the minimum.
func (s *providerState) learnSpacing(success bool, failure *usage.FetchFailure) {
	if !s.hasPolicy() {
		return
	}
	if usageRateLimited(failure) {
		s.spacing = min(s.policy.MaxSpacing, s.spacing*2)
		s.cleanStreak = 0
		return
	}
	if !success {
		return
	}
	s.cleanStreak++
	if s.cleanStreak < cleanStreakToStepDown {
		return
	}
	s.cleanStreak = 0
	if s.spacing > s.policy.MinSpacing {
		s.spacing = max(s.policy.MinSpacing, s.spacing*3/4)
	}
}

// usageRateLimited distinguishes the usage operation from auxiliary requests
// that share the provider's cooldown but not its spacing budget.
func usageRateLimited(failure *usage.FetchFailure) bool {
	return failure != nil &&
		failure.Kind == usage.FailureRateLimited &&
		failure.Operation == usage.OperationUsage &&
		failure.HTTPStatus == http.StatusTooManyRequests
}
