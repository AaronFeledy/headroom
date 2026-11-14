package claude

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
)

func New(opts Options) *Client {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	usageURL := opts.UsageURL
	if usageURL == "" {
		usageURL = defaultUsageURL
	}
	tokenURL := opts.TokenURL
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = "https://api.anthropic.com"
	}
	resetStateDir := opts.ResetStateDir
	if resetStateDir != "" {
		resetStateDir = filepath.Clean(resetStateDir)
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = providerName
	}
	account := opts.Account.Clone()
	// Banked-reset consumption is default-account-only: never give an extra
	// account a reset state directory even if a caller passes one.
	if account != nil {
		resetStateDir = ""
	}
	command := reauthCommandText
	signInCommand := &command
	if account != nil {
		signInCommand = nil
		if opts.ConfigDir != "" {
			command = accountSignInCommand(runtime.GOOS, expandConfigDir(opts.ConfigDir, opts.HomeDir))
			signInCommand = &command
		}
	}
	return &Client{
		client:        client,
		goos:          runtime.GOOS,
		keychain:      opts.Keychain,
		usageURL:      usageURL,
		tokenURL:      tokenURL,
		apiBaseURL:    apiBaseURL,
		name:          name,
		account:       account,
		signInCommand: signInCommand,
		resetStateDir: resetStateDir,
		paths: discoveryOptions{
			configuredPath: opts.CredentialsPath,
			configDir:      expandConfigDir(opts.ConfigDir, opts.HomeDir),
			homeDir:        opts.HomeDir,
			wslHomeDir:     opts.WSLHomeDir,
			openCodePath:   opts.OpenCodePath,
		},
	}
}
