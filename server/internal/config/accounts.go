package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var accountIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func validateAccounts(cfg Config, goos string) error {
	for name, provider := range cfg.Providers {
		if len(provider.Accounts) == 0 {
			continue
		}
		if len(provider.Accounts) > 8 {
			return fmt.Errorf("providers.claude.accounts[8]: more than 8 accounts: %w", ErrInvalidConfig)
		}
		seenIDs := map[string]int{}
		for i := range provider.Accounts {
			if err := validateAccountEntry(name, i, &provider); err != nil {
				return err
			}
			id := provider.Accounts[i].ID
			if prev, ok := seenIDs[id]; ok {
				return fmt.Errorf("providers.%s.accounts[%d]: duplicate id %q (also accounts[%d]): %w", name, i, id, prev, ErrInvalidConfig)
			}
			seenIDs[id] = i
			for prev := 0; prev < i; prev++ {
				if strings.EqualFold(provider.Accounts[prev].Label, provider.Accounts[i].Label) {
					return fmt.Errorf("providers.%s.accounts[%d]: duplicate label %q (also accounts[%d]): %w", name, i, provider.Accounts[i].Label, prev, ErrInvalidConfig)
				}
			}
		}
	}
	return validateAccountCredentialUniqueness(cfg, goos)
}

func validateAccountEntry(name string, index int, provider *ProviderConfig) error {
	prefix := fmt.Sprintf("providers.%s.accounts[%d]", name, index)
	account := &provider.Accounts[index]
	if !accountIDPattern.MatchString(account.ID) {
		return fmt.Errorf("%s: id %q is invalid: %w", prefix, account.ID, ErrInvalidConfig)
	}
	if err := validateAccountLabel(prefix, account.Label); err != nil {
		return err
	}
	if strings.TrimSpace(account.ConfigDir) == "" {
		return fmt.Errorf("%s: config_dir is required: %w", prefix, ErrInvalidConfig)
	}
	expanded, err := expandHomePath(account.ConfigDir)
	if err != nil {
		return fmt.Errorf("%s: config_dir: %v: %w", prefix, err, ErrInvalidConfig)
	}
	if !filepath.IsAbs(filepath.Clean(expanded)) {
		return fmt.Errorf("%s: config_dir must be absolute after ~ expansion: %w", prefix, ErrInvalidConfig)
	}
	// Keep the raw expanded spelling (trailing slash included) for Keychain and commands.
	account.ConfigDir = expanded
	return nil
}

func validateAccountLabel(prefix, label string) error {
	if label == "" || utf8.RuneCountInString(label) > 32 {
		return fmt.Errorf("%s: label must be 1-32 characters after trim: %w", prefix, ErrInvalidConfig)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s: label must not contain control characters: %w", prefix, ErrInvalidConfig)
		}
	}
	return nil
}

// expandHomePath replaces a leading ~ with $HOME without cleaning, so a trailing
// slash is preserved for Keychain service hashing.
func expandHomePath(path string) (string, error) {
	if path == "~" {
		return os.UserHomeDir()
	}
	if len(path) >= 2 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home + path[1:], nil
	}
	return path, nil
}

type accountLocation struct {
	file    string
	dir     string
	enabled bool
	source  string
}

func validateAccountCredentialUniqueness(cfg Config, goos string) error {
	claude := cfg.Providers["claude"]
	if len(claude.Accounts) == 0 {
		return nil
	}
	var refs []accountLocation
	if claude.Enabled {
		defaultFile, err := defaultClaudeCredentialsFile(claude.CredentialsPath)
		if err != nil {
			return err
		}
		defaultDir := ""
		if filepath.Base(defaultFile) == ".credentials.json" {
			defaultDir = filepath.Dir(defaultFile)
		}
		refs = append(refs, accountLocation{
			file: defaultFile, dir: defaultDir,
			enabled: true, source: "providers.claude",
		})
	}
	for i, account := range claude.Accounts {
		refs = append(refs, accountLocation{
			file:    filepath.Join(account.ConfigDir, ".credentials.json"),
			dir:     filepath.Clean(account.ConfigDir),
			enabled: account.Enabled,
			source:  fmt.Sprintf("providers.claude.accounts[%d]", i),
		})
	}
	return rejectDuplicateAccountLocations(refs, goos)
}

func defaultClaudeCredentialsFile(credentialsPath string) (string, error) {
	defaultPath := strings.TrimSpace(credentialsPath)
	if defaultPath == "" {
		defaultPath = "~/.claude/.credentials.json"
	}
	expanded, err := expandHomePath(defaultPath)
	if err != nil {
		return "", fmt.Errorf("providers.claude.credentials_path: %v: %w", err, ErrInvalidConfig)
	}
	return filepath.Clean(expanded), nil
}

func rejectDuplicateAccountLocations(refs []accountLocation, goos string) error {
	for i := 0; i < len(refs); i++ {
		if !refs[i].enabled {
			continue
		}
		for j := i + 1; j < len(refs); j++ {
			if !refs[j].enabled {
				continue
			}
			if sameAccountLocation(refs[i], refs[j], goos) {
				return fmt.Errorf("%s: config_dir duplicates %s: %w", refs[j].source, refs[i].source, ErrInvalidConfig)
			}
		}
	}
	return nil
}

func sameAccountLocation(a, b accountLocation, goos string) bool {
	if samePathString(a.file, b.file, goos) || sameExistingPath(a.file, b.file) {
		return true
	}
	if a.dir == "" || b.dir == "" {
		return false
	}
	return samePathString(a.dir, b.dir, goos) || sameExistingPath(a.dir, b.dir)
}

func samePathString(a, b, goos string) bool {
	return a == b || goos == "windows" && strings.EqualFold(a, b)
}

func sameExistingPath(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}
