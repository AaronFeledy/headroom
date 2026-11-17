package opencodego

import (
	"errors"
	"net/url"
)

const (
	keyExpiredMessage          = "OpenCode Go auth expired. Run `opencode auth login` again."
	consoleExpiredFormat       = "OpenCode console sign-in expired. Open OpenCode to refresh it, or run `%s`."
	consoleRejectedFormat      = "OpenCode Go rejected the OpenCode console sign-in. Run `%s` again."
	invalidCredentialsFormat   = "OpenCode Go credentials are invalid. Run `%s` again."
	ambiguousCredentialsFormat = "OpenCode Go credentials are ambiguous. Run `%s` again."
	notSignedInMessage         = "OpenCode Go is not signed in. Run `opencode auth login` and choose OpenCode Go."
	noWorkspaceMessage         = "Your OpenCode console sign-in has no active workspace. Run `opencode console switch` to choose one."
	otherOriginFormat          = "Your OpenCode console sign-in is for %s, so Headroom can't read OpenCode Go usage with it. Run `opencode auth login` to add an OpenCode Go API key."
	unparsedConsoleHost        = "another server"
)

// credentialKind is what a store holds, not how its display name was built.
// The two console kinds differ only in how they refresh: OpenCode 1.x keeps a
// console account it refreshes itself, while V2 credentials are ordinary
// stored tokens.
type credentialKind int

const (
	credentialKey credentialKind = iota
	credentialConsoleAccount
	credentialConsoleCredential
)

func (k credentialKind) console() bool { return k != credentialKey }

func (k credentialKind) sourceName(suffix string) string {
	if k == credentialKey {
		return "OpenCode Go CLI" + suffix
	}
	return "OpenCode console" + suffix
}

func (k credentialKind) signInCommand() string {
	if k == credentialConsoleAccount {
		return consoleSignInCommand
	}
	return signInCommandText
}

// checkedStatus maps a load result to the API's existing auth status
// vocabulary. A console account without a workspace, another origin, or a
// stale import marker is simply signed_out there, exactly like a missing key.
func checkedStatus(err error) string {
	switch {
	case err == nil:
		return "signed_in"
	case errors.Is(err, errCredentialExpired):
		return "expired"
	case errors.Is(err, errCredentialInvalid),
		errors.Is(err, errCredentialAmbiguous),
		errors.Is(err, errCredentialUnreadable):
		return "unreadable"
	default:
		return "signed_out"
	}
}

// consoleOriginError keeps only what may be shown from a console sign-in
// registered for another server: its hostname, never userinfo or a path.
type consoleOriginError struct {
	rawURL string
}

func (e *consoleOriginError) Error() string {
	return "opencodego: console sign-in is for " + e.host()
}

func (e *consoleOriginError) host() string {
	parsed, err := url.Parse(e.rawURL)
	if err != nil || parsed.Hostname() == "" {
		return unparsedConsoleHost
	}
	return parsed.Hostname()
}
