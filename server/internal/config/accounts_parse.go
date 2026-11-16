package config

import (
	"fmt"
	"strings"
	"unicode"
)

type rawAccountConfig struct {
	ID              *string `yaml:"id"`
	Label           *string `yaml:"label"`
	ConfigDir       *string `yaml:"config_dir"`
	CredentialsPath *string `yaml:"credentials_path"`
	Enabled         *bool   `yaml:"enabled"`
}

type rawProviderConfig struct {
	Enabled            *bool               `yaml:"enabled"`
	CredentialsPath    *string             `yaml:"credentials_path"`
	BrowserCredentials *bool               `yaml:"browser_credentials"`
	Accounts           *[]rawAccountConfig `yaml:"accounts"`
}

func applyProvider(name string, raw rawProviderConfig, cfg *Config) error {
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
	if raw.Accounts != nil {
		if name != "claude" {
			return fmt.Errorf("providers.%s.accounts[0]: accounts are supported only for claude: %w", name, ErrInvalidConfig)
		}
		accounts := make([]AccountConfig, 0, len(*raw.Accounts))
		for i, item := range *raw.Accounts {
			account := AccountConfig{Enabled: true}
			if item.ID != nil {
				if strings.IndexFunc(*item.ID, unicode.IsControl) >= 0 {
					return fmt.Errorf("providers.%s.accounts[%d]: id must not contain control characters: %w: %w", name, i, ErrInvalidConfig, ErrInvalidAccountID)
				}
				account.ID = strings.TrimSpace(*item.ID)
			}
			if item.Label != nil {
				account.Label = strings.TrimSpace(*item.Label)
			}
			if item.ConfigDir != nil {
				account.ConfigDir = *item.ConfigDir
			}
			if item.CredentialsPath != nil {
				account.legacyCredentialsPath = strings.TrimSpace(*item.CredentialsPath)
			}
			if item.Enabled != nil {
				account.Enabled = *item.Enabled
			}
			if account.Label == "" {
				account.Label = account.ID
			}
			accounts = append(accounts, account)
		}
		current.Accounts = accounts
	}
	cfg.Providers[name] = current
	return nil
}
