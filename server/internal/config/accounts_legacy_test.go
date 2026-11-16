package config_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_maps_legacy_account_credentials_path_to_config_dir(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workDir := filepath.Join(home, ".claude-work")
	// ~ expansion keeps the spelling after ~, so Windows keeps the forward slash.
	expandedWorkDir := home + "/.claude-work"

	tests := []struct {
		name     string
		account  string
		wantDir  string
		wantWarn int
	}{
		{
			name:     "absolute path",
			account:  "credentials_path: " + filepath.Join(workDir, ".credentials.json") + "\n",
			wantDir:  workDir,
			wantWarn: 1,
		},
		{
			name:     "home expansion",
			account:  "credentials_path: ~/.claude-work/.credentials.json\n",
			wantDir:  expandedWorkDir,
			wantWarn: 1,
		},
		{
			name:     "trailing slash adds none",
			account:  "credentials_path: ~/.claude-work/.credentials.json/\n",
			wantDir:  expandedWorkDir,
			wantWarn: 1,
		},
		{
			name:     "config_dir wins when consistent",
			account:  "config_dir: ~/.claude-work\n        credentials_path: ~/.claude-work/.credentials.json\n",
			wantDir:  expandedWorkDir,
			wantWarn: 0,
		},
		{
			name:     "config_dir wins when inconsistent",
			account:  "config_dir: ~/.claude-work\n        credentials_path: ~/.claude-side/.credentials.json\n",
			wantDir:  expandedWorkDir,
			wantWarn: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			logger, logs := migrationLogger()
			path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        "+tt.account)

			// When
			cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Logger: logger}, "linux")

			// Then
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			accounts := cfg.Providers["claude"].Accounts
			if len(accounts) != 1 {
				t.Fatalf("accounts = %d", len(accounts))
			}
			if accounts[0].ConfigDir != tt.wantDir {
				t.Fatalf("config_dir = %q, want %q", accounts[0].ConfigDir, tt.wantDir)
			}
			if got := strings.Count(logs.String(), "level=WARN"); got != tt.wantWarn {
				t.Fatalf("warnings = %d, want %d:\n%s", got, tt.wantWarn, logs)
			}
		})
	}

	t.Run("matches a current config_dir account", func(t *testing.T) {
		// Given
		legacyPath := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        credentials_path: ~/.claude-work/.credentials.json\n")
		currentPath := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        config_dir: ~/.claude-work\n")

		// When
		legacy, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", legacyPath}}, "linux")
		if err != nil {
			t.Fatal(err)
		}
		current, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", currentPath}}, "linux")
		if err != nil {
			t.Fatal(err)
		}

		// Then
		got, want := legacy.Providers["claude"].Accounts[0], current.Providers["claude"].Accounts[0]
		if got.ID != want.ID || got.Label != want.Label || got.Enabled != want.Enabled || got.ConfigDir != want.ConfigDir {
			t.Fatalf("legacy account = %+v, current account = %+v", got, want)
		}
		if file := filepath.Join(got.ConfigDir, ".credentials.json"); file != filepath.Join(home, ".claude-work", ".credentials.json") {
			t.Fatalf("credentials file = %q", file)
		}
	})
}

func Test_Load_rejects_legacy_account_paths_that_cannot_map(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct {
		name, credentialsPath, want string
	}{
		{"non-credentials base", "~/.claude-work/auth.json", "set config_dir"},
		{"relative path", ".claude-work/.credentials.json", "must be absolute"},
		{"relative with credentials base", "work/.credentials.json", "must be absolute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: "+tt.credentialsPath+"\n")
			_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
			if !errors.Is(err, config.ErrInvalidConfig) || !errors.Is(err, config.ErrInvalidAccountDir) {
				t.Fatalf("err = %v, want ErrInvalidConfig + ErrInvalidAccountDir", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func Test_Load_rejects_legacy_account_duplicating_default_credentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: true\n    credentials_path: ~/.claude/.credentials.json\n    accounts:\n      - id: work\n        credentials_path: ~/.claude/.credentials.json\n")
	_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
	if !errors.Is(err, config.ErrInvalidConfig) || !errors.Is(err, config.ErrDuplicateAccountDir) {
		t.Fatalf("err = %v, want duplicate config_dir", err)
	}
}

func Test_Load_warns_once_per_legacy_account(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	logger, logs := migrationLogger()
	path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n"+
		"      - id: work\n        credentials_path: ~/.claude-work/.credentials.json\n"+
		"      - id: side\n        credentials_path: ~/.claude-side/.credentials.json\n")

	// When
	if _, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Logger: logger}, "linux"); err != nil {
		t.Fatal(err)
	}

	// Then
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("warnings = %d, want one per account:\n%s", len(lines), logs)
	}
	for i, want := range []struct{ id, dir string }{
		{"account=work", home + "/.claude-work"},
		{"account=side", home + "/.claude-side"},
	} {
		if !strings.Contains(lines[i], "level=WARN") || !strings.Contains(lines[i], "deprecated credentials_path") || !strings.Contains(lines[i], want.id) {
			t.Fatalf("warning %d = %q, want deprecation for %s", i, lines[i], want.id)
		}
		// The text handler quotes the value, escaping Windows backslashes.
		if !strings.Contains(lines[i], "replacement="+strconv.Quote("config_dir: "+want.dir)) {
			t.Fatalf("warning %d = %q, want replacement config_dir: %s", i, lines[i], want.dir)
		}
	}
	if strings.Contains(logs.String(), ".credentials.json") {
		t.Fatalf("warning leaked the credentials file name:\n%s", logs)
	}
}
