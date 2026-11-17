package cli

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type UsageBucket struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

type Bucket struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
	StatusText  *string    `json:"status_text"`
	StartsAt    *time.Time `json:"starts_at"`
	DetailText  *string    `json:"detail_text"`
	Measurement *string    `json:"measurement"`
}

type Provider struct {
	Auth                  *ProviderAuth          `json:"auth"`
	ProviderName          string                 `json:"provider_name"`
	ProviderType          string                 `json:"-"`
	Account               *ProviderAccount       `json:"-"`
	PrimaryLabel          string                 `json:"primary_label"`
	SecondaryLabel        string                 `json:"secondary_label"`
	ShowSecondary         bool                   `json:"show_secondary"`
	Subtitle              *string                `json:"subtitle"`
	PrimaryStatusText     *string                `json:"primary_status_text"`
	SecondaryStatusText   *string                `json:"secondary_status_text"`
	ReauthCommand         *string                `json:"reauth_command"`
	Current               UsageBucket            `json:"current"`
	Weekly                UsageBucket            `json:"weekly"`
	Buckets               []Bucket               `json:"buckets"`
	Error                 *string                `json:"error"`
	NeedsReauth           bool                   `json:"needs_reauth"`
	IsSuccess             bool                   `json:"is_success"`
	RateLimitResetCredits *RateLimitResetCredits `json:"rate_limit_reset_credits"`
	UsageDetails          *UsageDetails          `json:"usage_details"`
	ResetAutomation       *ResetAutomation       `json:"reset_automation"`
	// AccountAbsent is true only when the server reports account_presence
	// "absent": it has never found a sign-in for this provider. Older servers,
	// unknown values, and malformed values leave the row shown.
	AccountAbsent bool `json:"-"`
}

type ProviderAuth struct {
	State         string  `json:"state"`
	SignInCommand *string `json:"sign_in_command"`
	SignInURL     *string `json:"sign_in_url"`
}

// ResetAutomation is the server-owned automatic reset state. Only the fields
// the text dashboard needs are decoded; --json passes the raw body through.
type ResetAutomation struct {
	Armed        bool
	WindowEndsAt *time.Time
}

type RateLimitResetCredits struct {
	AvailableCount     int64             `json:"available_count"`
	AccountFingerprint *string           `json:"account_fingerprint"`
	Kinds              []ResetCreditKind `json:"-"`
}

// ResetCreditKind is one reported kind of banked reset. Only the fields the
// text dashboard needs are decoded; other additive fields stay intact in the
// raw body that --json emits.
type ResetCreditKind struct {
	ID             string
	Label          string
	AvailableCount int64
}

type pace struct {
	available          bool
	expected, pressure float64
	label              string
	duration, step     time.Duration
}

func pacing(provider string, bucket Bucket, now time.Time) pace {
	if bucket.ResetsAt == nil || !bucket.ResetsAt.After(now) {
		return pace{label: "Pace unavailable"}
	}
	id, name := strings.ToLower(bucket.ID), strings.ToLower(provider)
	var duration time.Duration

	knownProvider := contains([]string{"claude", "codex", "cursor", "grok", "opencodego"}, name)
	switch {
	case knownProvider && (id == "weekly" || strings.HasPrefix(id, "weekly_")):
		duration = 7 * 24 * time.Hour
	case contains([]string{"claude", "codex", "opencodego"}, name) && id == "session":
		if strings.Contains(strings.ToLower(bucket.Label), "weekly") {
			duration = 7 * 24 * time.Hour
		} else {
			duration = 5 * time.Hour
		}
	case name == "cursor" && contains([]string{"session", "plan", "auto", "api", "on_demand"}, id):
		duration = 30 * 24 * time.Hour
	case name == "grok" && contains([]string{"session", "credits", "plan", "on_demand"}, id),
		name == "opencodego" && id == "monthly":
		start := previousMonth(*bucket.ResetsAt)
		duration = bucket.ResetsAt.Sub(start)
	}
	if start := bucket.StartsAt; start != nil && !start.IsZero() {
		reported := bucket.ResetsAt.Sub(*start)
		if reported > 0 && reported <= 366*24*time.Hour {
			duration = reported
		}
	}
	remaining := bucket.ResetsAt.Sub(now)
	if duration <= 0 || remaining > duration {
		return pace{label: "Pace unavailable"}
	}
	if statusOnly(bucket) {
		return pace{label: "Pace unavailable"}
	}
	expected := 100 * float64(duration-remaining) / float64(duration)
	difference := bucket.Utilization - expected
	points := int(math.Round(math.Abs(difference)))
	label := "On pace"
	if points != 0 {
		if difference > 0 {
			label = fmt.Sprintf("%d pp over pace", points)
		} else {
			label = fmt.Sprintf("%d pp under pace", points)
		}
	}
	timeRemaining, allowanceRemaining := 100-expected, 100-bucket.Utilization
	pressure := math.Max(0, math.Min(1, (timeRemaining-allowanceRemaining)/math.Max(0.000001, timeRemaining)))
	step := 7 * 24 * time.Hour
	if duration <= 24*time.Hour {
		step = time.Hour
	} else if duration <= 14*24*time.Hour {
		step = 24 * time.Hour
	}
	return pace{available: true, expected: expected, pressure: pressure, label: label, duration: duration, step: step}
}

func previousMonth(value time.Time) time.Time {
	value = value.UTC()
	first := time.Date(value.Year(), value.Month(), 1, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), time.UTC)
	previous := first.AddDate(0, -1, 0)
	day := min(value.Day(), first.AddDate(0, 0, -1).Day())
	return time.Date(previous.Year(), previous.Month(), day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), time.UTC)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func warningLevel(used float64, pace pace, previous int) int {
	enterPressure := [...]float64{0, .10, .25, .50}
	exitPressure := [...]float64{0, .08, .20, .40}
	enterCapacity := [...]float64{0, 95, 99, 100}
	exitCapacity := [...]float64{0, 94, 98, 99.5}
	enterFallback := [...]float64{0, 50, 75, 90}
	exitFallback := [...]float64{0, 45, 70, 85}
	result := 0
	for level := 1; level <= 3; level++ {
		recovering := previous >= level
		reached := false
		if pace.available {
			pressureThreshold, capacityThreshold := enterPressure[level], enterCapacity[level]
			if recovering {
				pressureThreshold, capacityThreshold = exitPressure[level], exitCapacity[level]
			}
			reached = pace.pressure >= pressureThreshold-1e-9 || used >= capacityThreshold
		} else {
			threshold := enterFallback[level]
			if recovering {
				threshold = exitFallback[level]
			}
			reached = used >= threshold
		}
		if reached {
			result = level
		}
	}
	return result
}

var warningNames = [...]string{"Normal", "Watch", "Warning", "Critical"}
