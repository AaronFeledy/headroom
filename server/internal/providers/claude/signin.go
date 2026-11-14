package claude

import (
	"errors"
	"regexp"
	"strings"
)

func keychainAccessError(err error) bool {
	return errors.Is(err, ErrKeychainLocked) || errors.Is(err, ErrKeychainUnavailable)
}

// needsClaudeCode reports Keychain states that signing in again can't fix: the
// Keychain must be unlocked, or Claude Code must refresh its own sign-in.
func needsClaudeCode(err error) bool {
	return keychainAccessError(err) || errors.Is(err, ErrKeychainExpired)
}

func isCredentialError(err error) bool {
	var credentialErr credentialsError
	return errors.As(err, &credentialErr) || errors.Is(err, ErrCredentialsMissing) || errors.Is(err, ErrCredentialsMalformed) || needsClaudeCode(err)
}

func (c *Client) credentialErrorMessage(err error) string {
	if errors.Is(err, ErrKeychainExpired) {
		command := reauthCommandText
		if c.signInCommand != nil {
			command = *c.signInCommand
		}
		return "Claude Code refreshes this sign-in when it runs. Use Claude Code once, or run `" + command + "`."
	}
	return formatCredentialError(err, c.goos)
}

var posixEnvDirSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func accountSignInCommand(goos, dir string) string {
	if goos == "windows" {
		escaped := strings.ReplaceAll(dir, "'", "''")
		return "$previous = $env:CLAUDE_CONFIG_DIR; $env:CLAUDE_CONFIG_DIR = '" + escaped + "'; try { claude auth login } finally { $env:CLAUDE_CONFIG_DIR = $previous }"
	}
	assignment := dir
	if !posixEnvDirSafe.MatchString(dir) {
		assignment = "'" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
	}
	return "CLAUDE_CONFIG_DIR=" + assignment + " claude auth login"
}
