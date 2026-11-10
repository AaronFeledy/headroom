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
	if account.CredentialsPath == "" {
		return fmt.Errorf("%s: credentials_path is required: %w", prefix, ErrInvalidConfig)
	}
	expanded, err := expandCredentialsPath(account.CredentialsPath)
	if err != nil {
		return fmt.Errorf("%s: credentials_path: %v: %w", prefix, err, ErrInvalidConfig)
	}
	if !filepath.IsAbs(expanded) {
		return fmt.Errorf("%s: credentials_path must be absolute after ~ expansion: %w", prefix, ErrInvalidConfig)
	}
	account.CredentialsPath = filepath.Clean(expanded)
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

func expandCredentialsPath(path string) (string, error) {
	if path == "~" {
		return os.UserHomeDir()
	}
	if len(path) >= 2 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func validateAccountCredentialUniqueness(cfg Config, goos string) error {
	claude := cfg.Providers["claude"]
	if len(claude.Accounts) == 0 {
		return nil
	}
	type pathRef struct {
		path    string
		enabled bool
		source  string
	}
	var refs []pathRef
	if claude.Enabled {
		defaultPath := strings.TrimSpace(claude.CredentialsPath)
		if defaultPath == "" {
			defaultPath = "~/.claude/.credentials.json"
		}
		expanded, err := expandCredentialsPath(defaultPath)
		if err != nil {
			return fmt.Errorf("providers.claude.credentials_path: %v: %w", err, ErrInvalidConfig)
		}
		refs = append(refs, pathRef{path: filepath.Clean(expanded), enabled: true, source: "providers.claude"})
	}
	for i, account := range claude.Accounts {
		refs = append(refs, pathRef{
			path:    account.CredentialsPath,
			enabled: account.Enabled,
			source:  fmt.Sprintf("providers.claude.accounts[%d]", i),
		})
	}
	for i := 0; i < len(refs); i++ {
		if !refs[i].enabled {
			continue
		}
		for j := i + 1; j < len(refs); j++ {
			if !refs[j].enabled {
				continue
			}
			if sameCredentialsPath(refs[i].path, refs[j].path, goos) {
				return fmt.Errorf("%s: credentials_path duplicates %s: %w", refs[j].source, refs[i].source, ErrInvalidConfig)
			}
		}
	}
	return nil
}

func sameCredentialsPath(a, b, goos string) bool {
	if a == b || goos == "windows" && strings.EqualFold(a, b) {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}
