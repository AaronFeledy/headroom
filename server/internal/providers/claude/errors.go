package claude

import (
	"errors"
	"fmt"
)

var (
	ErrCredentialsMissing   = errors.New("claude: credentials missing")
	ErrCredentialsMalformed = errors.New("claude: credentials malformed")
	ErrUpstream             = errors.New("claude: upstream error")
	ErrInvalidGrant         = errors.New("claude: invalid grant")
	ErrKeychainLocked       = errors.New("claude: keychain locked")
	ErrKeychainUnavailable  = errors.New("claude: keychain unavailable")
	// ErrRefreshUnavailable is returned when the sign-in is expired and Headroom
	// cannot refresh it (Keychain, or a credentials file that cannot be written).
	ErrRefreshUnavailable = errors.New("claude: sign-in expired and Headroom cannot refresh it")

	errClaudeCodeRefreshing = errors.New("claude: another process is refreshing credentials")
	errClaudeCodeLockLost   = errors.New("claude: oauth refresh lock lost")
)

type credentialsError struct {
	path string
	err  error
}

func (e credentialsError) Error() string {
	return fmt.Sprintf("claude credentials %s: %v", e.path, e.err)
}

func (e credentialsError) Unwrap() error { return e.err }
