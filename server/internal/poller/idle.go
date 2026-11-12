package poller

import (
	"slices"
	"sort"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// idleUtilizationCeiling is where idle slowdown stops being safe: near a limit
// the person is actively working and usage is likely to change.
const idleUtilizationCeiling = 90.0

type readingBucket struct {
	id          string
	utilization float64
	resetsAt    time.Time
}

// reading is the part of a successful response that decides whether the next
// scheduled poll can wait longer.
type reading struct {
	buckets      []readingBucket
	resetCredits int64
	hasCredits   bool
}

func readingOf(data usage.UsageData) reading {
	value := reading{buckets: make([]readingBucket, 0, len(data.Buckets))}
	for _, bucket := range data.Buckets {
		entry := readingBucket{id: bucket.ID, utilization: bucket.Utilization}
		if bucket.ResetsAt != nil {
			entry.resetsAt = bucket.ResetsAt.UTC()
		}
		value.buckets = append(value.buckets, entry)
	}
	sort.Slice(value.buckets, func(i, j int) bool { return value.buckets[i].id < value.buckets[j].id })
	if data.RateLimitResetCredits != nil {
		value.hasCredits = true
		value.resetCredits = data.RateLimitResetCredits.AvailableCount
	}
	return value
}

func (r reading) equal(other reading) bool {
	if r.hasCredits != other.hasCredits || r.resetCredits != other.resetCredits {
		return false
	}
	return slices.Equal(r.buckets, other.buckets)
}

// recordReading grows the idle delay only while readings repeat below the
// limit; any change, failure, or near-limit meter returns to the minimum.
func (s *providerState) recordReading(data usage.UsageData) {
	if !s.hasPolicy() {
		return
	}
	current := readingOf(data)
	if !s.hasReading || !s.lastReading.equal(current) || atPercentLimit(data) {
		s.idle = 0
	} else {
		s.idle = min(s.policy.IdleSpacing, max(s.spacing, s.idle)*3/2)
	}
	s.lastReading = current
	s.hasReading = true
}

// atPercentLimit ignores status-only rows: only real percent meters can say
// the person is actively close to a limit.
func atPercentLimit(data usage.UsageData) bool {
	for _, bucket := range data.Buckets {
		switch bucket.Measurement {
		case usage.MeasurementStatus:
			continue
		case "", usage.MeasurementPercent:
			if bucket.Utilization >= idleUtilizationCeiling {
				return true
			}
		}
	}
	return false
}
