package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"unicode"
)

// decodeResetCreditKinds treats absent, null, or malformed metadata as unknown
// so one bad entry cannot discard the usable count and fingerprint.
func decodeResetCreditKinds(raw json.RawMessage) []ResetCreditKind {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var entries []struct {
		ID             json.RawMessage `json:"id"`
		Label          json.RawMessage `json:"label"`
		AvailableCount json.RawMessage `json:"available_count"`
	}
	if json.Unmarshal(trimmed, &entries) != nil || len(entries) > 8 {
		return nil
	}
	kinds := make([]ResetCreditKind, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		var id, label string
		var count float64
		if json.Unmarshal(entry.ID, &id) != nil || !validResetKindID(id) || seen[id] {
			return nil
		}
		if json.Unmarshal(entry.Label, &label) != nil || !validResetKindLabel(label) {
			return nil
		}
		if json.Unmarshal(entry.AvailableCount, &count) != nil || count < 0 || count > 9007199254740991 || math.Trunc(count) != count {
			return nil
		}
		seen[id] = true
		kinds = append(kinds, ResetCreditKind{ID: id, Label: label, AvailableCount: int64(count)})
	}
	return kinds
}

func validResetKindID(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_') {
			return false
		}
	}
	return true
}

func validResetKindLabel(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
