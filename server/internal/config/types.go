package config

import (
	"errors"
	"log/slog"
	"time"
)

var (
	ErrInvalidConfig   = errors.New("config: invalid")
	ErrMalformedConfig = errors.New("config: malformed")
)

type Config struct {
	ListenAddr     string
	AuthToken      string
	PollInterval   time.Duration
	Providers      map[string]ProviderConfig
	DesktopSession bool
	SSHAccess      bool
	TLS            string
	TLSCertFile    string
	TLSKeyFile     string
	TLSDir         string
}

type ProviderConfig struct {
	Enabled            bool
	CredentialsPath    string
	BrowserCredentials bool
}

type LoadOptions struct {
	Args   []string
	Env    []string
	Logger *slog.Logger
}

func Defaults() Config {
	return Config{
		ListenAddr:   "127.0.0.1:7823",
		TLS:          "auto",
		PollInterval: 60 * time.Second,
		Providers: map[string]ProviderConfig{
			"claude": {Enabled: true, CredentialsPath: "~/.claude/.credentials.json"},
			"codex":  {Enabled: true},
			"cursor": {Enabled: true, BrowserCredentials: true},
			"grok":   {Enabled: true},
		},
	}
}
