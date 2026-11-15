package claude

import (
	"errors"
	"regexp"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func keychainAccessError(err error) bool {
	return errors.Is(err, ErrKeychainLocked) || errors.Is(err, ErrKeychainUnavailable)
}

func isCredentialError(err error) bool {
	var credentialErr credentialsError
	return errors.As(err, &credentialErr) || errors.Is(err, ErrCredentialsMissing) || errors.Is(err, ErrCredentialsMalformed) || errors.Is(err, ErrRefreshUnavailable) || keychainAccessError(err)
}

func (c *Client) credentialErrorMessage(err error) string {
	if errors.Is(err, ErrRefreshUnavailable) {
		command := reauthCommandText
		if c.signInCommand != nil {
			command = *c.signInCommand
		}
		name := "Claude Code"
		c.stateMu.Lock()
		loaded := c.loaded
		c.stateMu.Unlock()
		if loaded != nil && loaded.source == credentialSourceOpenCode {
			name = "OpenCode"
		}
		var refused *credstore.NotWritableError
		if errors.As(err, &refused) {
			return "Sign-in expired, and Headroom can't save a refreshed one because " + refused.Reason.Phrase() + ". Use " + name + " once, or run `" + command + "`."
		}
		return name + " refreshes this sign-in when it runs. Use " + name + " once, or run `" + command + "`."
	}
	return formatCredentialError(err, c.goos)
}

var posixEnvDirSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func credentialSourceName(path string, source credentialSource) string {
	name := "Claude Code"
	if source == credentialSourceOpenCode {
		name = "OpenCode"
	}
	if strings.HasPrefix(path, `\\wsl.`) {
		name += " (WSL)"
	}
	return name
}

func (c *Client) attachAuth(result *usage.UsageData, expired, commandless bool) {
	c.stateMu.Lock()
	creds := c.loaded
	checked := append([]usage.AuthChecked(nil), c.checked...)
	c.stateMu.Unlock()
	state := "signed_out"
	var source *usage.AuthSource
	if creds != nil {
		source = &usage.AuthSource{Kind: "cli", Name: credentialSourceName(creds.path, creds.source)}
		state = "signed_in"
		if result.NeedsReauth || expired {
			state = "expired"
			for i := range checked {
				if checked[i].Status == "signed_in" {
					checked[i].Status = "expired"
				}
			}
		}
	}
	auth := usage.NewAuth(providerName, state, source)
	auth.Checked = checked
	if c.account != nil && auth.SignInCommand != nil {
		auth.SignInCommand = c.signInCommand
	}
	if commandless {
		auth.SignInCommand = nil
		result.ReauthCommand = nil
	}
	result.Auth = auth
}

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
