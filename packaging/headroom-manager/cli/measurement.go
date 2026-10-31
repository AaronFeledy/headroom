package cli

import (
	"strings"
	"time"
)

func statusOnly(bucket Bucket) bool {
	if bucket.Measurement != nil {
		return *bucket.Measurement != "percent"
	}
	return strings.EqualFold(bucket.ID, "on_demand") && bucket.Utilization <= 0 && bucket.StatusText != nil &&
		strings.TrimSpace(*bucket.StatusText) != "" && !strings.Contains(*bucket.StatusText, " / ")
}

func weeklyBucket(provider string, bucket Bucket) bool {
	if bucket.ID == "weekly" || strings.HasPrefix(bucket.ID, "weekly_") {
		return true
	}
	if !strings.EqualFold(provider, "Cursor") || statusOnly(bucket) || bucket.StartsAt == nil || bucket.ResetsAt == nil {
		return false
	}
	duration := bucket.ResetsAt.Sub(*bucket.StartsAt)
	return !bucket.StartsAt.IsZero() && duration >= 6*24*time.Hour && duration <= 8*24*time.Hour
}
