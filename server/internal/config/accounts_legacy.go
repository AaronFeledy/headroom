package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// legacyAccountConfigDir maps the deprecated 3.5 credentials_path spelling onto
// the config_dir it named. The directory keeps expandHomePath's spelling (only
// the file name and its separator removed, no trailing slash added) because
// Keychain service names and sign-in commands use config_dir verbatim;
// filepath.Dir would clean it and, on Windows, rewrite its separators.
func legacyAccountConfigDir(prefix, credentialsPath string) (string, error) {
	expanded, err := expandHomePath(credentialsPath)
	if err != nil {
		return "", fmt.Errorf("%s: credentials_path: %v: %w: %w", prefix, err, ErrInvalidConfig, ErrInvalidAccountDir)
	}
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("%s: credentials_path must be absolute after ~ expansion; set config_dir to the directory used as CLAUDE_CONFIG_DIR at sign-in: %w: %w", prefix, ErrInvalidConfig, ErrInvalidAccountDir)
	}
	trimmed := strings.TrimRight(expanded, `/\`)
	if filepath.Base(trimmed) != ".credentials.json" {
		return "", fmt.Errorf("%s: credentials_path must be <config_dir>/.credentials.json; set config_dir to the directory used as CLAUDE_CONFIG_DIR at sign-in: %w: %w", prefix, ErrInvalidConfig, ErrInvalidAccountDir)
	}
	dir := trimmed[:len(trimmed)-len(".credentials.json")]
	if parent := dir[:len(dir)-1]; filepath.IsAbs(parent) {
		// Keep the separator of a root such as / or C:\.
		dir = parent
	}
	return dir, nil
}

// legacyCredentialsMatchesConfigDir reports whether the deprecated
// credentials_path names the file inside config_dir, so both-present configs
// only warn when the two disagree.
func legacyCredentialsMatchesConfigDir(credentialsPath, configDir string) bool {
	expanded, err := expandHomePath(credentialsPath)
	if err != nil {
		return false
	}
	want := filepath.Join(configDir, ".credentials.json")
	return filepath.Clean(strings.TrimRight(expanded, `/\`)) == filepath.Clean(want)
}

func logAccountWarning(logger *slog.Logger, accountID, message, replacement string) {
	if logger == nil {
		return
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn, message,
		slog.String("account", accountID),
		slog.String("replacement", replacement))
}
