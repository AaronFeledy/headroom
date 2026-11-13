package opencodego

import (
	"encoding/json"
	"math"
	"time"
	"unicode"
)

type storedCredential struct {
	Type    string   `json:"type"`
	Key     string   `json:"key"`
	Access  string   `json:"access"`
	Expires *float64 `json:"expires"`
}

func decodeCredentialValue(value string, now time.Time) (credential, error) {
	var stored storedCredential
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return credential{}, errCredentialInvalid
	}
	switch stored.Type {
	case "key":
		if !validToken(stored.Key) {
			return credential{}, errCredentialInvalid
		}
		return credential{token: stored.Key}, nil
	case "oauth":
		if stored.Expires == nil || !validExpiry(*stored.Expires) {
			return credential{}, errCredentialInvalid
		}
		// Zero means the issuer reported no lifetime, so the token never expires.
		var expiresAt time.Time
		if *stored.Expires != 0 {
			expiresAt = time.UnixMilli(int64(*stored.Expires)).UTC()
			if !now.Before(expiresAt) {
				return credential{}, errCredentialExpired
			}
		}
		if !validToken(stored.Access) {
			return credential{}, errCredentialInvalid
		}
		return credential{token: stored.Access, expiresAt: expiresAt}, nil
	default:
		return credential{}, errCredentialInvalid
	}
}

func decodeLegacyAuth(data []byte) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", errCredentialInvalid
	}
	raw, ok := root["opencode-go"]
	if !ok {
		return "", errCredentialMissing
	}
	var entry struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", errCredentialInvalid
	}
	if entry.Type != "api" {
		return "", errCredentialMissing
	}
	if !validToken(entry.Key) {
		return "", errCredentialInvalid
	}
	return entry.Key, nil
}

func validExpiry(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value == math.Trunc(value) && value <= math.MaxInt64
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
