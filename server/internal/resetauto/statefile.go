package resetauto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// Strict field formats for the durable authorization file.
var (
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	resetKindPattern   = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	eventPattern       = regexp.MustCompile(`^[a-z_]{1,32}$`)
	// completedOutcomePattern accepts the provider outcomes Headroom reports.
	completedOutcomePattern = regexp.MustCompile(`^(reset|already_redeemed|nothing_to_reset|no_credit|unavailable)$`)
)

type stateFile struct {
	Version   int                     `json:"version"`
	Providers map[string]storedRecord `json:"providers"`
}

type storedRecord struct {
	ArmedAt            *string      `json:"armed_at"`
	WindowEndsAt       *string      `json:"window_ends_at"`
	Kinds              []string     `json:"kinds"`
	AccountFingerprint *string      `json:"account_fingerprint"`
	LastEvent          *storedEvent `json:"last_event"`
}

type storedEvent struct {
	At      string  `json:"at"`
	Event   string  `json:"event"`
	Outcome *string `json:"outcome"`
	Reason  *string `json:"reason"`
	Kind    *string `json:"kind"`
}

func (r Record) stored(provider string) (storedRecord, error) {
	stored := storedRecord{LastEvent: storedEventOf(r.LastEvent)}
	if len(r.Kinds) > 0 {
		if provider != ProviderClaude || len(r.Kinds) > maxArmKinds {
			return storedRecord{}, errInvalidState
		}
		stored.Kinds = append([]string(nil), r.Kinds...)
	}
	if !r.Armed() {
		return stored, nil
	}
	armedAt := formatStateTime(r.ArmedAt)
	windowEndsAt := formatStateTime(r.WindowEndsAt)
	fingerprint := r.AccountFingerprint
	stored.ArmedAt, stored.WindowEndsAt, stored.AccountFingerprint = &armedAt, &windowEndsAt, &fingerprint
	if provider == ProviderClaude && len(stored.Kinds) == 0 {
		return storedRecord{}, errInvalidState
	}
	return stored, nil
}

func (s storedRecord) record(provider string) (Record, error) {
	if provider != ProviderClaude && provider != ProviderCodex {
		return Record{}, errInvalidState
	}
	event, err := s.LastEvent.event()
	if err != nil {
		return Record{}, err
	}
	record := Record{LastEvent: event}
	if s.ArmedAt == nil {
		if s.WindowEndsAt != nil || s.AccountFingerprint != nil || s.Kinds != nil {
			return Record{}, errInvalidState
		}
		return record, nil
	}
	if s.WindowEndsAt == nil || s.AccountFingerprint == nil {
		return Record{}, errInvalidState
	}
	armedAt, err := parseStateTime(*s.ArmedAt)
	if err != nil {
		return Record{}, errInvalidState
	}
	windowEndsAt, err := parseStateTime(*s.WindowEndsAt)
	if err != nil || !fingerprintPattern.MatchString(*s.AccountFingerprint) {
		return Record{}, errInvalidState
	}
	record.ArmedAt, record.WindowEndsAt = armedAt, windowEndsAt
	record.AccountFingerprint = *s.AccountFingerprint
	if len(s.Kinds) > 0 {
		if provider != ProviderClaude || len(s.Kinds) > maxArmKinds {
			return Record{}, errInvalidState
		}
		seen := make(map[string]bool, len(s.Kinds))
		for _, id := range s.Kinds {
			if !resetKindPattern.MatchString(id) || seen[id] {
				return Record{}, errInvalidState
			}
			seen[id] = true
		}
		record.Kinds = append([]string(nil), s.Kinds...)
	} else if provider == ProviderClaude {
		return Record{}, errInvalidState
	}
	return record, nil
}

func decodeState(data []byte) (map[string]Record, error) {
	if err := validateStrictJSON(data); err != nil {
		return nil, ErrStateUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file stateFile
	if err := decoder.Decode(&file); err != nil {
		return nil, ErrStateUnavailable
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrStateUnavailable
	}
	if file.Version != stateVersion {
		return nil, ErrStateUnavailable
	}
	records := make(map[string]Record, len(file.Providers))
	for provider, stored := range file.Providers {
		record, err := stored.record(provider)
		if err != nil {
			return nil, ErrStateUnavailable
		}
		records[provider] = record
	}
	return records, nil
}

// validateStrictJSON rejects duplicate object keys before decoding; Go's
// encoding/json otherwise accepts the last value silently.
func validateStrictJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateStrictValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidState
	}
	return nil
}

func validateStrictValue(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errInvalidState
	}
	token, err := decoder.Token()
	if err != nil {
		return errInvalidState
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return errInvalidState
			}
			seen[key] = true
			if err := validateStrictValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errInvalidState
		}
	case '[':
		for decoder.More() {
			if err := validateStrictValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errInvalidState
		}
	default:
		return errInvalidState
	}
	return nil
}

func (s *storedEvent) event() (*usage.ResetAutomationEvent, error) {
	if s == nil {
		return nil, nil
	}
	if _, err := parseStateTime(s.At); err != nil {
		return nil, errInvalidState
	}
	switch s.Event {
	case "requested", "completed", "failed", "canceled":
	default:
		return nil, errInvalidState
	}
	if s.Kind != nil && !eventPattern.MatchString(*s.Kind) {
		return nil, errInvalidState
	}
	if s.Reason != nil && !eventPattern.MatchString(*s.Reason) {
		return nil, errInvalidState
	}
	if s.Outcome != nil && !completedOutcomePattern.MatchString(*s.Outcome) {
		return nil, errInvalidState
	}
	return &usage.ResetAutomationEvent{At: s.At, Event: s.Event, Outcome: s.Outcome, Reason: s.Reason, Kind: s.Kind}, nil
}

func storedEventOf(event *usage.ResetAutomationEvent) *storedEvent {
	if event == nil {
		return nil
	}
	return &storedEvent{At: event.At, Event: event.Event, Outcome: event.Outcome, Reason: event.Reason, Kind: event.Kind}
}

func newEvent(now time.Time, event, outcome, reason, kind string) *usage.ResetAutomationEvent {
	value := &usage.ResetAutomationEvent{At: formatStateTime(now), Event: event}
	if outcome != "" {
		value.Outcome = &outcome
	}
	if reason != "" {
		value.Reason = &reason
	}
	if kind != "" {
		value.Kind = &kind
	}
	return value
}

func formatStateTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func parseStateTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || formatStateTime(parsed) != value {
		return time.Time{}, errInvalidState
	}
	return parsed, nil
}
