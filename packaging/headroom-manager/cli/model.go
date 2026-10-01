package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

type Provider struct {
	Auth                  *ProviderAuth          `json:"auth"`
	ProviderName          string                 `json:"provider_name"`
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
}

type ProviderAuth struct {
	State         string  `json:"state"`
	SignInCommand *string `json:"sign_in_command"`
	SignInURL     *string `json:"sign_in_url"`
}

type RateLimitResetCredits struct {
	AvailableCount     int64   `json:"available_count"`
	AccountFingerprint *string `json:"account_fingerprint"`
}

func decodeUsage(body []byte) ([]Provider, error) {
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("usage server returned an invalid collection")
	}
	if err := validateUniqueJSON(body); err != nil {
		return nil, errors.New("usage server returned invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var providers []Provider
	if err := decoder.Decode(&providers); err != nil || len(providers) > 64 {
		return nil, errors.New("usage server returned invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("usage server returned extra JSON")
	}
	var rawProviders []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawProviders); err != nil || len(rawProviders) != len(providers) {
		return nil, errors.New("usage server returned invalid JSON")
	}
	seen := map[string]bool{}
	for index := range providers {
		provider := &providers[index]
		raw := rawProviders[index]
		if _, ok := raw["error"]; !ok {
			return nil, errors.New("usage server omitted provider status")
		}
		if _, ok := raw["is_success"]; !ok {
			return nil, errors.New("usage server omitted provider status")
		}
		if _, ok := raw["needs_reauth"]; !ok {
			return nil, errors.New("usage server omitted provider status")
		}
		for _, field := range []string{"is_success", "needs_reauth"} {
			value := string(bytes.TrimSpace(raw[field]))
			if value != "true" && value != "false" {
				return nil, errors.New("usage server returned invalid provider status")
			}
		}
		bucketsJSON, hasBuckets := raw["buckets"]
		if hasBuckets && (len(bytes.TrimSpace(bucketsJSON)) == 0 || bytes.TrimSpace(bucketsJSON)[0] != '[') {
			return nil, errors.New("usage server returned an invalid bucket contract")
		}
		if hasBuckets && provider.Error == nil {
			var rawBuckets []map[string]json.RawMessage
			if json.Unmarshal(bucketsJSON, &rawBuckets) != nil {
				return nil, errors.New("usage server returned invalid meters")
			}
			for _, bucket := range rawBuckets {
				var used *float64
				if json.Unmarshal(bucket["utilization"], &used) != nil || used == nil {
					return nil, errors.New("usage server omitted meter utilization")
				}
			}
		}
		provider.ProviderName = strings.TrimSpace(provider.ProviderName)
		key := strings.ToLower(provider.ProviderName)
		if key == "" || seen[key] || provider.IsSuccess != (provider.Error == nil) {
			return nil, errors.New("usage server returned an invalid provider")
		}
		seen[key] = true
		if provider.Error != nil {
			provider.Buckets = []Bucket{}
			provider.RateLimitResetCredits = nil
		}
		if provider.Error == nil && !hasBuckets {
			if strings.TrimSpace(provider.PrimaryLabel) == "" {
				return nil, errors.New("usage server omitted its bucket contract")
			}
			provider.Buckets = append(provider.Buckets, Bucket{ID: "session", Label: provider.PrimaryLabel,
				Utilization: provider.Current.Utilization, ResetsAt: provider.Current.ResetsAt, StatusText: provider.PrimaryStatusText})
			if provider.ShowSecondary {
				provider.Buckets = append(provider.Buckets, Bucket{ID: "weekly", Label: provider.SecondaryLabel,
					Utilization: provider.Weekly.Utilization, ResetsAt: provider.Weekly.ResetsAt, StatusText: provider.SecondaryStatusText})
			}
		}
		if provider.RateLimitResetCredits != nil {
			credits := provider.RateLimitResetCredits
			if credits.AvailableCount < 0 || credits.AvailableCount > 9007199254740991 {
				provider.RateLimitResetCredits = nil
			}
			if credits.AccountFingerprint != nil && !validFingerprint(*credits.AccountFingerprint) {
				credits.AccountFingerprint = nil
			}
		}
		if len(provider.Buckets) > 12 {
			return nil, errors.New("usage server returned too many buckets")
		}
		bucketIDs := map[string]bool{}
		for bucketIndex := range provider.Buckets {
			bucket := &provider.Buckets[bucketIndex]
			if bucket.ID == "" || bucketIDs[bucket.ID] || strings.TrimSpace(bucket.Label) == "" ||
				math.IsNaN(bucket.Utilization) || math.IsInf(bucket.Utilization, 0) || bucket.Utilization < 0 || bucket.Utilization > 100 {
				return nil, errors.New("usage server returned an invalid bucket")
			}
			bucketIDs[bucket.ID] = true
			if bucket.StatusText == nil || strings.TrimSpace(*bucket.StatusText) == "" {
				id := strings.ToLower(bucket.ID)
				if (id == "api" || bucketIndex == 0 && id != "auto") && provider.PrimaryStatusText != nil {
					bucket.StatusText = provider.PrimaryStatusText
				} else if (id == "weekly" || id == "on_demand") && provider.SecondaryStatusText != nil {
					bucket.StatusText = provider.SecondaryStatusText
				}
			}
		}
	}
	return providers, nil
}

func (credits *RateLimitResetCredits) UnmarshalJSON(data []byte) error {
	*credits = RateLimitResetCredits{AvailableCount: -1}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	var count *float64
	if json.Unmarshal(fields["available_count"], &count) != nil || count == nil || *count < 0 || *count > 9007199254740991 || math.Trunc(*count) != *count {
		return nil
	}
	credits.AvailableCount = int64(*count)
	_ = json.Unmarshal(fields["account_fingerprint"], &credits.AccountFingerprint)
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
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

	if !contains([]string{"claude", "codex", "cursor", "grok"}, name) {
		return pace{label: "Pace unavailable"}
	}
	switch {
	case id == "weekly" || strings.HasPrefix(id, "weekly_"):
		duration = 7 * 24 * time.Hour
	case (name == "claude" || name == "codex") && id == "session":
		if strings.Contains(strings.ToLower(bucket.Label), "weekly") {
			duration = 7 * 24 * time.Hour
		} else {
			duration = 5 * time.Hour
		}
	case name == "cursor" && contains([]string{"session", "plan", "auto", "api", "on_demand"}, id):
		duration = 30 * 24 * time.Hour
	case name == "grok" && contains([]string{"session", "credits", "plan", "on_demand"}, id):
		start := previousMonth(*bucket.ResetsAt)
		duration = bucket.ResetsAt.Sub(start)
	}
	remaining := bucket.ResetsAt.Sub(now)
	if duration <= 0 || remaining > duration {
		return pace{label: "Pace unavailable"}
	}
	if id == "on_demand" && bucket.Utilization <= 0 && bucket.StatusText != nil && *bucket.StatusText != "" && !strings.Contains(*bucket.StatusText, " / ") {
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
	if duration == 5*time.Hour {
		step = time.Hour
	} else if duration == 7*24*time.Hour {
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

func validateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return errors.New("invalid JSON object")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
