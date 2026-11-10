package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_parses_claude_accounts_with_defaults_and_home_expansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "providers:\n  claude:\n    accounts:\n" +
		"      - id: \"  work  \"\n        credentials_path: \"  ~/.claude-work/../.claude-work/.credentials.json  \"\n" +
		"      - id: side\n        label: \"  Side Project  \"\n" +
		"        credentials_path: " + filepath.Join(home, "abs", "creds.json") + "\n" +
		"        enabled: false\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	accounts := cfg.Providers["claude"].Accounts
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d", len(accounts))
	}
	if accounts[0].ID != "work" || accounts[0].Label != "work" || !accounts[0].Enabled {
		t.Fatalf("work account = %+v", accounts[0])
	}
	wantWork := filepath.Clean(filepath.Join(home, ".claude-work", ".credentials.json"))
	if accounts[0].CredentialsPath != wantWork {
		t.Fatalf("work path = %q, want %q", accounts[0].CredentialsPath, wantWork)
	}
	if accounts[1].Label != "Side Project" || accounts[1].Enabled {
		t.Fatalf("side account = %+v", accounts[1])
	}
	if got, want := accounts[1].CredentialsPath, filepath.Join(home, "abs", "creds.json"); got != want {
		t.Fatalf("side path = %q, want %q", got, want)
	}
}

func Test_Load_default_config_has_no_accounts(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", filepath.Join(t.TempDir(), "missing.yaml")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers["claude"].Accounts) != 0 {
		t.Fatalf("default accounts = %v", cfg.Providers["claude"].Accounts)
	}
}

func Test_Load_env_credentials_path_does_not_touch_accounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: ~/.claude-work/.credentials.json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{
		Args: []string{"-config", path},
		Env:  []string{"USAGE_PROVIDER_CLAUDE_CREDENTIALS_PATH=/env/claude.json"},
	}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["claude"].CredentialsPath != "/env/claude.json" {
		t.Fatalf("default path = %q", cfg.Providers["claude"].CredentialsPath)
	}
	if len(cfg.Providers["claude"].Accounts) != 1 || cfg.Providers["claude"].Accounts[0].ID != "work" {
		t.Fatalf("accounts mutated: %+v", cfg.Providers["claude"].Accounts)
	}
	if got := cfg.Providers["claude"].Accounts[0].CredentialsPath; got != filepath.Join(home, ".claude-work", ".credentials.json") {
		t.Fatalf("account path = %q, want original YAML path", got)
	}
}

func writeAccountConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func Test_Load_rejects_invalid_accounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultCreds := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(defaultCreds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultCreds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	var many strings.Builder
	many.WriteString("providers:\n  claude:\n    accounts:\n")
	for i := 0; i < 9; i++ {
		id := "a" + string(rune('0'+i))
		many.WriteString("      - id: " + id + "\n")
		many.WriteString("        credentials_path: " + filepath.Join(home, id+".json") + "\n")
	}

	abs := func(name string) string { return filepath.Join(home, name) }

	tests := []struct {
		name string
		yaml string
	}{
		{"non-claude", "providers:\n  codex:\n    accounts:\n      - id: work\n        credentials_path: " + abs("x.json") + "\n"},
		{"non-claude-empty", "providers:\n  cursor:\n    accounts: []\n"},
		{"too-many", many.String()},
		{"dup-id", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: " + abs("a.json") + "\n      - id: work\n        label: Other\n        credentials_path: " + abs("b.json") + "\n"},
		{"dup-label", "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        credentials_path: " + abs("a.json") + "\n      - id: side\n        label: work\n        credentials_path: " + abs("b.json") + "\n"},
		{"unicode-dup-label", "providers:\n  claude:\n    accounts:\n      - id: work\n        label: S\n        credentials_path: " + abs("a.json") + "\n      - id: side\n        label: ſ\n        credentials_path: " + abs("b.json") + "\n"},
		{"dup-account-path", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: " + abs("a.json") + "\n      - id: side\n        credentials_path: " + abs("a.json") + "\n"},
		{"empty-path", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: \"\"\n"},
		{"whitespace-path", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: \"   \"\n"},
		{"relative-path", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: relative/creds.json\n"},
		{"dup-default-path", "providers:\n  claude:\n    enabled: true\n    credentials_path: ~/.claude/.credentials.json\n    accounts:\n      - id: work\n        credentials_path: ~/.claude/.credentials.json\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeAccountConfig(t, tt.yaml)
			_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			if err == nil || !strings.Contains(err.Error(), "providers.") {
				t.Fatalf("error should name providers path: %v", err)
			}
		})
	}

	t.Run("missing-file-ok", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: "+abs("missing-work.json")+"\n")
		if _, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disabled-dup-ok", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: true\n    credentials_path: ~/.claude/.credentials.json\n    accounts:\n      - id: work\n        enabled: false\n        credentials_path: ~/.claude/.credentials.json\n")
		if _, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("windows-case-insensitive-path", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        credentials_path: "+abs("Work.json")+"\n      - id: side\n        credentials_path: "+abs("work.json")+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "windows")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v, want duplicate path error", err)
		}
	})

	t.Run("symlink-dup", func(t *testing.T) {
		link := filepath.Join(home, "linked-creds.json")
		if err := os.Symlink(defaultCreds, link); err != nil {
			t.Fatal(err)
		}
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: true\n    credentials_path: ~/.claude/.credentials.json\n    accounts:\n      - id: work\n        credentials_path: "+link+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	})
}
