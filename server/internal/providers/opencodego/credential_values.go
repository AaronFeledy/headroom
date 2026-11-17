package opencodego

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type storedCredential struct {
	Type     string              `json:"type"`
	Key      string              `json:"key"`
	Access   string              `json:"access"`
	Expires  *float64            `json:"expires"`
	Metadata *credentialMetadata `json:"metadata"`
}

// credentialMetadata is the console context V2 stores beside an OAuth token.
type credentialMetadata struct {
	Server *string `json:"server"`
	OrgID  string  `json:"orgID"`
}

// storedKind classifies a credential-table value without requiring it to be
// usable, so an expired or invalid console row is still reported as console.
func storedKind(value string) credentialKind {
	var stored storedCredential
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return credentialKey
	}
	if stored.Type == "oauth" {
		return credentialConsoleCredential
	}
	if stored.Type == "key" && stored.Metadata != nil && stored.Metadata.OrgID != "" {
		return credentialConsoleCredential
	}
	return credentialKey
}

func decodeCredentialValue(value string, now time.Time, usageURL string) (credential, credentialKind, error) {
	var stored storedCredential
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return credential{}, credentialKey, errCredentialInvalid
	}
	switch stored.Type {
	case "key":
		kind := storedKind(value)
		if !validToken(stored.Key) {
			return credential{}, kind, errCredentialInvalid
		}
		creds := credential{token: stored.Key}
		if kind == credentialConsoleCredential {
			orgID, err := consoleMetadata(stored.Metadata, usageURL)
			if err != nil {
				return credential{}, kind, err
			}
			creds.orgID = orgID
		}
		return creds, kind, nil
	case "oauth":
		if stored.Expires == nil || !validExpiry(*stored.Expires) {
			return credential{}, credentialConsoleCredential, errCredentialInvalid
		}
		// Zero means the issuer reported no lifetime, so the token never expires.
		var expiresAt time.Time
		if *stored.Expires != 0 {
			expiresAt = time.UnixMilli(int64(*stored.Expires)).UTC()
			if !now.Before(expiresAt) {
				return credential{}, credentialConsoleCredential, errCredentialExpired
			}
		}
		if !validToken(stored.Access) {
			return credential{}, credentialConsoleCredential, errCredentialInvalid
		}
		orgID, err := consoleMetadata(stored.Metadata, usageURL)
		if err != nil {
			return credential{}, credentialConsoleCredential, err
		}
		return credential{token: stored.Access, orgID: orgID, expiresAt: expiresAt}, credentialConsoleCredential, nil
	default:
		return credential{}, credentialKey, errCredentialInvalid
	}
}

// consoleMetadata validates the server and organization a V2 console token
// carries. A token for another server is simply not a candidate.
func consoleMetadata(metadata *credentialMetadata, usageURL string) (string, error) {
	if metadata == nil {
		return "", nil
	}
	// A console token belongs only to the provider's own origin.
	if metadata.Server != nil && !sameOrigin(*metadata.Server, usageURL) {
		return "", errCredentialMissing
	}
	if metadata.OrgID == "" {
		return "", nil
	}
	if !validToken(metadata.OrgID) {
		return "", errCredentialMissing
	}
	return metadata.OrgID, nil
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

// sameOrigin compares the scheme and host, including any port, of two URLs.
func sameOrigin(left string, right string) bool {
	leftURL, err := url.Parse(left)
	if err != nil || leftURL.Scheme == "" || leftURL.Host == "" {
		return false
	}
	rightURL, err := url.Parse(right)
	if err != nil || rightURL.Scheme == "" || rightURL.Host == "" {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) && strings.EqualFold(leftURL.Host, rightURL.Host)
}
