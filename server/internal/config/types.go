package config

import (
	"errors"
	"log/slog"
	"time"
)

var (
	ErrInvalidConfig         = errors.New("config: invalid")
	ErrMalformedConfig       = errors.New("config: malformed")
	ErrDuplicateAccountID    = errors.New("config: duplicate account id")
	ErrDuplicateAccountLabel = errors.New("config: duplicate account label")
	ErrInvalidAccountID      = errors.New("config: invalid account id")
	ErrInvalidAccountLabel   = errors.New("config: invalid account label")
	ErrInvalidAccountDir     = errors.New("config: invalid account config_dir")
	ErrDuplicateAccountDir   = errors.New("config: duplicate account config_dir")
	ErrTooManyAccounts       = errors.New("config: too many accounts")
)

type Config struct {
	// ConfigPath is the absolute resolved path read by Load.
	ConfigPath     string
	ListenAddr     string
	AuthToken      string
	PollInterval   time.Duration
	Providers      map[string]ProviderConfig
	DesktopSession bool
	CheckConfig    bool
	SSHAccess      bool
	TLS            string
	TLSCertFile    string
	TLSKeyFile     string
	TLSDir         string
	ResetStateDir  string
	// ResetAutomationDir holds the server-owned automatic reset authorization.
	ResetAutomationDir string
}

type AccountConfig struct {
	ID    string
	Label string
	// ConfigDir is the CLAUDE_CONFIG_DIR used at sign-in. After load it holds the
	// raw expanded spelling (~ replaced, separators and trailing slash kept) for
	// Keychain hashing and sign-in commands. File access and uniqueness use the
	// cleaned path.
	ConfigDir string
	Enabled   bool
}

type ProviderConfig struct {
	Enabled            bool
	CredentialsPath    string
	BrowserCredentials bool
	Accounts           []AccountConfig
}

type LoadOptions struct {
	Args   []string
	Env    []string
	Logger *slog.Logger
	// Dir is the server's working directory for resolving relative credential
	// paths during validation. Empty means this process's working directory.
	Dir string
}

func Defaults() Config {
	return Config{
		ListenAddr:   "127.0.0.1:7823",
		TLS:          "auto",
		PollInterval: 60 * time.Second,
		Providers: map[string]ProviderConfig{
			"claude":     {Enabled: true},
			"codex":      {Enabled: true},
			"cursor":     {Enabled: true, BrowserCredentials: true},
			"grok":       {Enabled: true},
			"opencodego": {Enabled: true},
		},
	}
}
