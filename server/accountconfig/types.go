// Package accountconfig provides validated, atomic edits to extra Claude accounts.
package accountconfig

import (
	"errors"
	"regexp"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

// Account is an extra Claude account, with ConfigDir spelled as used at sign-in.
// Add and CheckAdd honor Enabled; callers adding an active account set it true.
type Account struct {
	ID, Label, ConfigDir string
	Enabled              bool
}

// AccountChanges selects the account fields to update; nil leaves a field unchanged.
type AccountChanges struct {
	Label   *string
	Enabled *bool
}

// Target identifies the file and the server overrides used to validate its contents.
// Path takes precedence over any --config argument or USAGE_CONFIG environment value.
// Dir is the server's working directory for relative paths; empty means this process's.
type Target struct {
	Path      string
	Args, Env []string
	Dir       string
}

// Change is the committed edit and the exact bytes required for a CAS rollback.
// Before is nil when the config did not exist; Restore then writes an empty config.
type Change struct {
	Path          string
	Before, After []byte
	Account       Account
}

var (
	// ErrNoChanges means neither Label nor Enabled was requested.
	ErrNoChanges          = errors.New("no account changes requested")
	ErrDuplicateID        = errors.New("a Claude account with this id already exists")
	ErrDuplicateLabel     = errors.New("a Claude account with this label already exists (labels ignore case)")
	ErrDuplicateConfigDir = errors.New("this Claude config directory is already used by another Claude account or the default Claude sign-in")
	ErrTooManyAccounts    = errors.New("Headroom supports at most 8 extra Claude accounts")
	ErrInvalidID          = errors.New(`invalid account id: use 1-32 lowercase letters, digits, "-", or "_", starting with a letter or digit`)
	ErrInvalidLabel       = errors.New("invalid account label: use 1-32 characters without control characters")
	ErrInvalidConfigDir   = errors.New("invalid Claude config directory: use an absolute path or one starting with ~")
	ErrAccountNotFound    = errors.New("Claude account not found")
	ErrUnsupportedLayout  = errors.New("unsupported config layout; edit providers.claude.accounts manually")
	ErrConfigChanged      = errors.New("config changed since account edit; rollback refused")
)

// ValidationError reports a rejected effective config. Its message is authored
// from the error category, a line number, or an account index, never from
// server error text, so config contents such as keys and tokens stay private.
type ValidationError struct{ cause error }

var (
	yamlLine        = regexp.MustCompile(`line (\d+):`)
	accountLocation = regexp.MustCompile(`providers\.claude\.accounts\[(\d+)\]`)
)

func (e *ValidationError) Error() string {
	text := e.cause.Error()
	if errors.Is(e.cause, config.ErrMalformedConfig) {
		if match := yamlLine.FindStringSubmatch(text); match != nil {
			return "the server config could not be parsed (line " + match[1] + ")"
		}
		return "the server config could not be parsed"
	}
	for _, rule := range accountRules {
		if errors.Is(e.cause, rule.server) {
			location := "an existing Claude account"
			if match := accountLocation.FindStringSubmatch(text); match != nil {
				location = match[0]
			}
			return "invalid server configuration: " + location + ": " + rule.public.Error()
		}
	}
	return "invalid server configuration; headroom serve reports the full error"
}

// Unwrap preserves the cause for errors.Is without exposing it in Error.
func (e *ValidationError) Unwrap() error { return e.cause }
