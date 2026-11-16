package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ResolvePath returns the absolute path Load would read, including legacy migration.
func ResolvePath(opts LoadOptions) (string, error) {
	flags, err := parseFlags(opts.Args)
	if err != nil {
		return "", err
	}
	return resolvePath(opts, flags, systemMigration(opts))
}

func systemMigration(opts LoadOptions) configMigration {
	return configMigration{goos: runtime.GOOS, logger: opts.Logger, ops: migrationOps{rename: os.Rename, symlink: os.Symlink, link: os.Link}}
}

func resolvePath(opts LoadOptions, flags flagValues, migration configMigration) (string, error) {
	path, err := migration.resolve(flags, opts.Env)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// Check builds the effective configuration for data using Load's overrides and validation.
func Check(data []byte, opts LoadOptions) (Config, error) {
	flags, err := parseFlags(opts.Args)
	if err != nil {
		return Config{}, err
	}
	path, err := resolvePath(opts, flags, systemMigration(opts))
	if err != nil {
		return Config{}, err
	}
	return buildConfig(data, opts, buildContext{goos: runtime.GOOS, path: path, flags: flags})
}

type buildContext struct {
	goos  string
	path  string
	flags flagValues
}

func buildConfig(data []byte, opts LoadOptions, source buildContext) (Config, error) {
	cfg := Defaults()
	cfg.ConfigPath = source.path
	if err := applyYAML(data, source.path, &cfg); err != nil {
		return Config{}, err
	}
	if err := applyEnv(parseEnv(opts.Env), &cfg); err != nil {
		return Config{}, err
	}
	if err := applyFlags(source.flags, &cfg); err != nil {
		return Config{}, err
	}
	if defaultPath, err := DefaultPath(source.goos, opts.Env); err == nil {
		cfg.TLSDir = tlsDirectory(defaultPath)
		cfg.ResetStateDir = filepath.Join(filepath.Dir(defaultPath), "claude-reset-state")
		cfg.ResetAutomationDir = filepath.Join(filepath.Dir(defaultPath), "reset-automation")
	}
	if err := validateAccounts(cfg, source.goos, opts); err != nil {
		return Config{}, err
	}
	if err := validateTLS(cfg); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval <= 0 {
		return Config{}, fmt.Errorf("poll interval must be positive: %w", ErrInvalidConfig)
	}
	return cfg, nil
}
