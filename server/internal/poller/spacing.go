package poller

import (
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func providerMinInterval(provider usage.Provider) time.Duration {
	if limited, ok := provider.(usage.MinIntervalProvider); ok {
		if value := limited.MinPollInterval(); value > 0 {
			return value
		}
	}
	return 0
}

// effectiveInterval is the configured poll interval raised to the provider's
// declared minimum upstream spacing.
func (s *providerState) effectiveInterval() time.Duration {
	if s.minInterval > s.interval {
		return s.minInterval
	}
	return s.interval
}

// insideSpacingWindow reports whether the last attempt that received an HTTP
// response is still younger than the provider's declared minimum.
func (s *providerState) insideSpacingWindow(now time.Time) bool {
	return s.minInterval > 0 && !s.responseAt.IsZero() && now.Before(s.responseAt.Add(s.minInterval))
}
