package config_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_released_example_configs(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, tag := range []string{"v3.4.0", "v3.5.0", "v3.6.0"} {
		t.Run(tag, func(t *testing.T) {
			// When
			path := filepath.Join("testdata", "config.example."+tag+".yaml")
			cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")

			// Then
			if err != nil {
				t.Fatalf("Load %s: %v", path, err)
			}
			accounts := cfg.Providers["claude"].Accounts
			if len(accounts) != 1 {
				t.Fatalf("accounts = %d, want the extra account", len(accounts))
			}
			account := accounts[0]
			if account.ID != "work" || account.Label != "Work" || !account.Enabled {
				t.Fatalf("account = %+v", account)
			}
			want := filepath.Join(home, ".claude-work", ".credentials.json")
			if got := filepath.Join(account.ConfigDir, ".credentials.json"); got != want {
				t.Fatalf("credentials file = %q, want %q", got, want)
			}
		})
	}
}
