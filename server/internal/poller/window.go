package poller

import (
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// windowResetTolerance is how far a reset time may move between readings and
// still describe the same window. The shortest window is hours long, so a
// larger change starts a new one.
const windowResetTolerance = time.Minute

// keepWindowResets carries a window's first reset time forward while later
// readings place it within windowResetTolerance. It copies the bucket slice so
// the provider's returned data is never modified.
func keepWindowResets(previous, current usage.UsageData) usage.UsageData {
	current.Current.ResetsAt = keepResetTime(previous.Current.ResetsAt, current.Current.ResetsAt)
	current.Weekly.ResetsAt = keepResetTime(previous.Weekly.ResetsAt, current.Weekly.ResetsAt)
	if current.Buckets != nil {
		buckets := make([]usage.Bucket, len(current.Buckets))
		copy(buckets, current.Buckets)
		for index := range buckets {
			var previousAt *time.Time
			for _, bucket := range previous.Buckets {
				if bucket.ID == buckets[index].ID && bucket.ResetsAt != nil {
					previousAt = bucket.ResetsAt
					break
				}
			}
			buckets[index].ResetsAt = keepResetTime(previousAt, buckets[index].ResetsAt)
		}
		current.Buckets = buckets
	}
	return current
}

func keepResetTime(previous, current *time.Time) *time.Time {
	if previous != nil && current != nil && current.Sub(*previous).Abs() <= windowResetTolerance {
		return previous
	}
	return current
}

// sameCredentialEpoch treats a null epoch as an unknown credential context,
// which never inherits an older reading's window.
func sameCredentialEpoch(previous, current *string) bool {
	return previous != nil && current != nil && *previous == *current
}
