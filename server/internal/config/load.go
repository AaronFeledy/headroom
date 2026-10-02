package config

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type rawConfig struct {
	ListenAddr   *string                      `yaml:"listen_addr"`
	AuthToken    *string                      `yaml:"auth_token"`
	PollInterval *string                      `yaml:"poll_interval"`
	SSHAccess    *bool                        `yaml:"ssh_access"`
	Providers    map[string]rawProviderConfig `yaml:"providers"`
	TLS          *string                      `yaml:"tls"`
	TLSCertFile  *string                      `yaml:"tls_cert_file"`
	TLSKeyFile   *string                      `yaml:"tls_key_file"`
}

type rawProviderConfig struct {
	Enabled            *bool   `yaml:"enabled"`
	CredentialsPath    *string `yaml:"credentials_path"`
	BrowserCredentials *bool   `yaml:"browser_credentials"`
}

type flagValues struct {
	ConfigPath     string
	ListenAddr     string
	AuthToken      string
	PollInterval   string
	DesktopSession bool
	SSHAccess      bool
	Set            map[string]bool
	TLS            string
	TLSCertFile    string
	TLSKeyFile     string
}

func Load(ctx context.Context, opts LoadOptions) (Config, error) {
	return loadForOS(ctx, opts, runtime.GOOS, migrationOps{rename: os.Rename, symlink: os.Symlink, link: os.Link})
}

func loadForOS(ctx context.Context, opts LoadOptions, goos string, ops migrationOps) (Config, error) {
	select {
	case <-ctx.Done():
		return Config{}, fmt.Errorf("load config canceled: %w", ctx.Err())
	default:
	}

	flags, err := parseFlags(opts.Args)
	if err != nil {
		return Config{}, err
	}
	env := parseEnv(opts.Env)
	path, err := (configMigration{goos: goos, ops: ops, logger: opts.Logger}).resolve(flags, opts.Env)
	if err != nil {
		return Config{}, err
	}

	cfg := Defaults()
	if err := applyYAML(path, &cfg); err != nil {
		return Config{}, err
	}
	if err := applyEnv(env, &cfg); err != nil {
		return Config{}, err
	}
	if err := applyFlags(flags, &cfg); err != nil {
		return Config{}, err
	}
	if defaultPath, err := DefaultPath(goos, opts.Env); err == nil {
		cfg.TLSDir = tlsDirectory(defaultPath)
	}
	if err := validateTLS(cfg); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval <= 0 {
		return Config{}, fmt.Errorf("poll interval must be positive: %w", ErrInvalidConfig)
	}
	return cfg, nil
}

func parseFlags(args []string) (flagValues, error) {
	values := flagValues{Set: map[string]bool{}}
	fs := flag.NewFlagSet("usage-server", flag.ContinueOnError)
	fs.StringVar(&values.ConfigPath, "config", "", "path to config.yaml")
	fs.StringVar(&values.ListenAddr, "listen-addr", "", "address for the HTTP server")
	fs.StringVar(&values.AuthToken, "auth-token", "", "optional bearer token")
	fs.StringVar(&values.TLS, "tls", "", "TLS mode: auto, on, off")
	fs.StringVar(&values.TLSCertFile, "tls-cert-file", "", "TLS certificate PEM file")
	fs.StringVar(&values.TLSKeyFile, "tls-key-file", "", "TLS private key PEM file")
	fs.StringVar(&values.PollInterval, "poll-interval", "", "provider poll interval")
	fs.BoolVar(&values.DesktopSession, "desktop-session", false, "start a private desktop TLS session")
	fs.BoolVar(&values.SSHAccess, "ssh-access", false, "enable access through the private SSH Unix socket")
	if err := fs.Parse(args); err != nil {
		return flagValues{}, fmt.Errorf("parse flags: %w", err)
	}
	fs.Visit(func(f *flag.Flag) { values.Set[f.Name] = true })
	return values, nil
}

func applyYAML(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config %s: %w", path, errors.Join(ErrMalformedConfig, err))
	}
	return applyRaw(raw, cfg)
}

func applyRaw(raw rawConfig, cfg *Config) error {
	if raw.TLS != nil {
		cfg.TLS = *raw.TLS
	}
	if raw.TLSCertFile != nil {
		cfg.TLSCertFile = *raw.TLSCertFile
	}
	if raw.TLSKeyFile != nil {
		cfg.TLSKeyFile = *raw.TLSKeyFile
	}
	if raw.ListenAddr != nil {
		cfg.ListenAddr = *raw.ListenAddr
	}
	if raw.AuthToken != nil {
		cfg.AuthToken = *raw.AuthToken
	}
	if raw.PollInterval != nil {
		duration, err := parseDuration("poll_interval", *raw.PollInterval)
		if err != nil {
			return err
		}
		cfg.PollInterval = duration
	}
	if raw.SSHAccess != nil {
		cfg.SSHAccess = *raw.SSHAccess
	}
	for name, provider := range raw.Providers {
		applyProvider(strings.ToLower(name), provider, cfg)
	}
	return nil
}

func applyProvider(name string, raw rawProviderConfig, cfg *Config) {
	current := cfg.Providers[name]
	if raw.BrowserCredentials != nil {
		current.BrowserCredentials = *raw.BrowserCredentials
	}
	if raw.Enabled != nil {
		current.Enabled = *raw.Enabled
	}
	if raw.CredentialsPath != nil {
		current.CredentialsPath = *raw.CredentialsPath
	}
	cfg.Providers[name] = current
}

func applyEnv(env map[string]string, cfg *Config) error {
	if value := env["USAGE_TLS"]; value != "" {
		cfg.TLS = value
	}
	if value := env["USAGE_TLS_CERT_FILE"]; value != "" {
		cfg.TLSCertFile = value
	}
	if value := env["USAGE_TLS_KEY_FILE"]; value != "" {
		cfg.TLSKeyFile = value
	}
	if value := env["USAGE_LISTEN_ADDR"]; value != "" {
		cfg.ListenAddr = value
	}
	if value := env["USAGE_AUTH_TOKEN"]; value != "" {
		cfg.AuthToken = value
	}
	if value := env["USAGE_POLL_INTERVAL"]; value != "" {
		duration, err := parseDuration("USAGE_POLL_INTERVAL", value)
		if err != nil {
			return err
		}
		cfg.PollInterval = duration
	}
	if value, ok := env["USAGE_SSH_ACCESS"]; ok && value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("USAGE_SSH_ACCESS %q: %w", value, errors.Join(ErrInvalidConfig, err))
		}
		cfg.SSHAccess = enabled
	}
	return applyProviderEnv(env, cfg)
}

func applyProviderEnv(env map[string]string, cfg *Config) error {
	for key, value := range env {
		if strings.HasPrefix(key, "USAGE_PROVIDER_") && strings.HasSuffix(key, "_BROWSER_CREDENTIALS") {
			name := providerNameFromEnv(key, "USAGE_PROVIDER_", "_BROWSER_CREDENTIALS")
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, errors.Join(ErrInvalidConfig, err))
			}
			current := cfg.Providers[name]
			current.BrowserCredentials = enabled
			cfg.Providers[name] = current
		}
		if strings.HasPrefix(key, "USAGE_PROVIDER_") && strings.HasSuffix(key, "_ENABLED") {
			name := providerNameFromEnv(key, "USAGE_PROVIDER_", "_ENABLED")
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("%s %q: %w", key, value, errors.Join(ErrInvalidConfig, err))
			}
			current := cfg.Providers[name]
			current.Enabled = enabled
			cfg.Providers[name] = current
		}
		if strings.HasPrefix(key, "USAGE_PROVIDER_") && strings.HasSuffix(key, "_CREDENTIALS_PATH") {
			name := providerNameFromEnv(key, "USAGE_PROVIDER_", "_CREDENTIALS_PATH")
			current := cfg.Providers[name]
			current.CredentialsPath = value
			cfg.Providers[name] = current
		}
	}
	return nil
}

func applyFlags(flags flagValues, cfg *Config) error {
	if flags.Set["tls"] {
		cfg.TLS = flags.TLS
	}
	if flags.Set["tls-cert-file"] {
		cfg.TLSCertFile = flags.TLSCertFile
	}
	if flags.Set["tls-key-file"] {
		cfg.TLSKeyFile = flags.TLSKeyFile
	}
	if flags.Set["desktop-session"] {
		cfg.DesktopSession = flags.DesktopSession
	}
	if flags.Set["ssh-access"] {
		cfg.SSHAccess = flags.SSHAccess
	}
	if flags.Set["listen-addr"] {
		cfg.ListenAddr = flags.ListenAddr
	}
	if flags.Set["auth-token"] {
		cfg.AuthToken = flags.AuthToken
	}
	if flags.Set["poll-interval"] {
		duration, err := parseDuration("poll-interval", flags.PollInterval)
		if err != nil {
			return err
		}
		cfg.PollInterval = duration
	}
	return nil
}

func parseDuration(name string, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, value, errors.Join(ErrInvalidConfig, err))
	}
	return duration, nil
}
