package opencodego

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type usageEnvelope struct {
	Usage *usageWindows `json:"usage"`
}

type usageWindows struct {
	Rolling *usageWindow `json:"rolling"`
	Weekly  *usageWindow `json:"weekly"`
	Monthly *usageWindow `json:"monthly"`
}

type usageWindow struct {
	Percent    *float64 `json:"percent"`
	ResetInSec *float64 `json:"resetInSec"`
	ResetsAt   string   `json:"resetsAt"`
	ResetAt    string   `json:"resetAt"`
}

// decodeUsage maps the upstream percentage windows. The upstream reports
// used-percent in 0..100 units, so a reported 0.5 is half a percent and is
// never multiplied. A missing rolling window is a parse failure; weekly and
// monthly are optional and simply omit their bucket.
func decodeUsage(body []byte, now time.Time) ([]usage.Bucket, error) {
	var envelope usageEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode OpenCode Go usage: %w", err)
	}
	if envelope.Usage == nil || envelope.Usage.Rolling == nil {
		return nil, errUsageResponse
	}
	buckets := make([]usage.Bucket, 0, 3)
	for _, window := range []struct {
		value *usageWindow
		id    string
		label string
	}{
		{envelope.Usage.Rolling, usage.BucketSession, "5-Hour"},
		{envelope.Usage.Weekly, usage.BucketWeekly, "Weekly"},
		{envelope.Usage.Monthly, usage.BucketMonthly, "Monthly"},
	} {
		if window.value == nil {
			continue
		}
		bucket, err := windowBucket(window.value, window.id, window.label, now)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, nil
}

func windowBucket(window *usageWindow, id string, label string, now time.Time) (usage.Bucket, error) {
	if window == nil || window.Percent == nil {
		return usage.Bucket{}, errUsageResponse
	}
	percent := *window.Percent
	if math.IsNaN(percent) || math.IsInf(percent, 0) {
		return usage.Bucket{}, errUsageResponse
	}
	bucket := usage.Bucket{
		ID:          id,
		Label:       label,
		Utilization: math.Max(0, math.Min(100, percent)),
		Measurement: usage.MeasurementPercent,
	}
	switch {
	case window.ResetInSec != nil:
		seconds := math.Max(0, *window.ResetInSec)
		reset := now.Add(time.Duration(seconds * float64(time.Second))).UTC()
		bucket.ResetsAt = &reset
	case window.ResetsAt != "":
		bucket.ResetsAt = parseResetTime(window.ResetsAt)
	case window.ResetAt != "":
		bucket.ResetsAt = parseResetTime(window.ResetAt)
	}
	return bucket, nil
}

func parseResetTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}
