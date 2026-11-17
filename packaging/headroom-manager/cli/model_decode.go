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

// Timing metadata is optional; a malformed start must not hide usable meters.
func (bucket *Bucket) UnmarshalJSON(data []byte) error {
	type bucketJSON Bucket
	var decoded struct {
		bucketJSON
		StartsAt    json.RawMessage `json:"starts_at"`
		DetailText  json.RawMessage `json:"detail_text"`
		Measurement json.RawMessage `json:"measurement"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*bucket = Bucket(decoded.bucketJSON)
	if len(decoded.Measurement) > 0 && string(decoded.Measurement) != "null" {
		measurement := "unknown"
		if err := json.Unmarshal(decoded.Measurement, &measurement); err != nil {
			measurement = "unknown"
		}
		bucket.Measurement = &measurement
	}
	bucket.DetailText = optionalDetail[string](decoded.DetailText)
	var start time.Time
	if json.Unmarshal(decoded.StartsAt, &start) == nil && !start.IsZero() {
		bucket.StartsAt = &start
	}
	return nil
}

// UnmarshalJSON tolerates malformed display metadata. A bad time never hides
// the armed marker, and the countdown falls back to its short form.
func (automation *ResetAutomation) UnmarshalJSON(data []byte) error {
	*automation = ResetAutomation{}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	var armed bool
	if json.Unmarshal(fields["armed"], &armed) == nil {
		automation.Armed = armed
	}
	var endsAt time.Time
	if json.Unmarshal(fields["window_ends_at"], &endsAt) == nil && !endsAt.IsZero() {
		automation.WindowEndsAt = &endsAt
	}
	return nil
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
		provider.ProviderType = decodeProviderType(raw["provider_type"])
		provider.Account = decodeProviderAccount(raw["account"])
		var presence string
		provider.AccountAbsent = json.Unmarshal(raw["account_presence"], &presence) == nil && presence == "absent"
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
			provider.UsageDetails = nil
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
	credits.Kinds = decodeResetCreditKinds(fields["kinds"])
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

func validateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
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
