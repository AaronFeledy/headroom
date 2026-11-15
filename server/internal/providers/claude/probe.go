package claude

import (
	"context"
	"errors"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// SignInProbe describes local discovery without refreshing credentials or contacting Claude.
type SignInProbe struct {
	State   string
	Source  *usage.AuthSource
	Checked []usage.AuthChecked
}

// ProbeSignIn reads the current credential candidates in discovery order without writes or HTTP.
// Only a non-refreshable Keychain credential can be expired; file credentials remain refreshable.
func (c *Client) ProbeSignIn(ctx context.Context) SignInProbe {
	result := SignInProbe{State: "signed_out", Checked: []usage.AuthChecked{}}
	var unavailable *usage.AuthSource
	for _, candidate := range c.credentialCandidates() {
		snapshot, err := candidate.store.load(ctx)
		entry := candidate.checked
		entry.Status = "unreadable"
		switch {
		case errors.Is(err, ErrCredentialsMissing):
			entry.Status = "signed_out"
		case errors.Is(err, ErrKeychainLocked):
			entry.Status = "locked"
		case err == nil:
			parsed, parseErr := parseCredentials(candidate.key, candidate.source, snapshot)
			if parseErr == nil {
				entry.Status = "signed_in"
				if _, writable := candidate.store.(writableCredentialStore); !writable && !parsed.expiresAt.After(time.Now().UTC().Add(expirySkew)) {
					entry.Status = "expired"
				}
			}
		}
		result.Checked = append(result.Checked, entry)
		source := &usage.AuthSource{Kind: entry.Kind, Name: entry.Name}
		switch {
		case errors.Is(err, ErrCredentialsMissing):
			continue
		case keychainAccessError(err):
			unavailable = source
			result.State = entry.Status
			continue
		default:
			result.State, result.Source = entry.Status, source
			return result
		}
	}
	result.Source = unavailable
	return result
}
