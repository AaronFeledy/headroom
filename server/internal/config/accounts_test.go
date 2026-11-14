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
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "providers:\n  claude:\n    accounts:\n" +
		"      - id: \"  work  \"\n        config_dir: \"~/.claude-work/../.claude-work\"\n" +
		"      - id: side\n        label: \"  Side Project  \"\n" +
		"        config_dir: " + filepath.Join(home, "abs", "side") + "\n" +
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
	// Raw expanded spelling: ~ replaced, no Clean, so ../ remains until callers clean.
	wantWork := home + "/.claude-work/../.claude-work"
	if accounts[0].ConfigDir != wantWork {
		t.Fatalf("work config_dir = %q, want %q", accounts[0].ConfigDir, wantWork)
	}
	if accounts[1].Label != "Side Project" || accounts[1].Enabled {
		t.Fatalf("side account = %+v", accounts[1])
	}
	if got, want := accounts[1].ConfigDir, filepath.Join(home, "abs", "side"); got != want {
		t.Fatalf("side config_dir = %q, want %q", got, want)
	}
}

func Test_Load_config_dir_keeps_trailing_slash_after_home_expansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: \"~/x/\"\n")
	cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Providers["claude"].Accounts[0].ConfigDir
	want := home + "/x/"
	if got != want {
		t.Fatalf("config_dir = %q, want raw trailing slash %q", got, want)
	}
}

func Test_Load_default_config_has_no_accounts_and_empty_claude_credentials_path(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", filepath.Join(t.TempDir(), "missing.yaml")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers["claude"].Accounts) != 0 {
		t.Fatalf("default accounts = %v", cfg.Providers["claude"].Accounts)
	}
	if cfg.Providers["claude"].CredentialsPath != "" {
		t.Fatalf("default CredentialsPath = %q, want empty for discovery", cfg.Providers["claude"].CredentialsPath)
	}
}

func Test_Load_env_credentials_path_does_not_touch_accounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: ~/.claude-work\n"
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
	if got := cfg.Providers["claude"].Accounts[0].ConfigDir; got != home+"/.claude-work" {
		t.Fatalf("account config_dir = %q, want original YAML path", got)
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
	t.Setenv("USERPROFILE", home)
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
		many.WriteString("        config_dir: " + filepath.Join(home, id) + "\n")
	}

	abs := func(name string) string { return filepath.Join(home, name) }

	tests := []struct {
		name string
		yaml string
	}{
		{"non-claude", "providers:\n  codex:\n    accounts:\n      - id: work\n        config_dir: " + abs("x") + "\n"},
		{"non-claude-empty", "providers:\n  cursor:\n    accounts: []\n"},
		{"too-many", many.String()},
		{"dup-id", "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: " + abs("a") + "\n      - id: work\n        label: Other\n        config_dir: " + abs("b") + "\n"},
		{"dup-label", "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        config_dir: " + abs("a") + "\n      - id: side\n        label: work\n        config_dir: " + abs("b") + "\n"},
		{"unicode-dup-label", "providers:\n  claude:\n    accounts:\n      - id: work\n        label: S\n        config_dir: " + abs("a") + "\n      - id: side\n        label: ſ\n        config_dir: " + abs("b") + "\n"},
		{"dup-account-dir", "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: " + abs("a") + "\n      - id: side\n        config_dir: " + abs("a") + "\n"},
		{"empty-dir", "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: \"\"\n"},
		{"whitespace-dir", "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: \"   \"\n"},
		{"relative-dir", "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: relative/side\n"},
		{"legacy-credentials-path", "providers:\n  claude:\n    accounts:\n      - id: work\n        credentials_path: ~/.claude-work/.credentials.json\n"},
		{"dup-default-dir", "providers:\n  claude:\n    enabled: true\n    accounts:\n      - id: work\n        config_dir: ~/.claude\n"},
		{"dup-default-explicit-file", "providers:\n  claude:\n    enabled: true\n    credentials_path: ~/.claude-alt/.credentials.json\n    accounts:\n      - id: work\n        config_dir: ~/.claude-alt\n"},
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
			if tt.name == "legacy-credentials-path" && !strings.Contains(err.Error(), "config_dir is required") {
				t.Fatalf("legacy credentials_path error = %v, want config_dir is required", err)
			}
		})
	}

	t.Run("missing-file-ok", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: "+abs("missing-work")+"\n")
		if _, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disabled-dup-ok", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: true\n    accounts:\n      - id: work\n        enabled: false\n        config_dir: ~/.claude\n")
		if _, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("windows-case-insensitive-dir", func(t *testing.T) {
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        config_dir: "+abs("Work")+"\n      - id: side\n        config_dir: "+abs("work")+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "windows")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v, want duplicate path error", err)
		}
	})

	t.Run("symlink-dir-dup", func(t *testing.T) {
		realDir := filepath.Join(home, "real-work")
		if err := os.MkdirAll(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, "linked-work")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        config_dir: "+realDir+"\n      - id: side\n        config_dir: "+link+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("hardlink-credentials-file-dup", func(t *testing.T) {
		dirA := filepath.Join(home, "hard-a")
		dirB := filepath.Join(home, "hard-b")
		if err := os.MkdirAll(dirA, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dirB, 0o700); err != nil {
			t.Fatal(err)
		}
		fileA := filepath.Join(dirA, ".credentials.json")
		fileB := filepath.Join(dirB, ".credentials.json")
		if err := os.WriteFile(fileA, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(fileA, fileB); err != nil {
			t.Fatal(err)
		}
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        config_dir: "+dirA+"\n      - id: side\n        config_dir: "+dirB+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v, want hard-linked credential file duplicate", err)
		}
	})

	t.Run("symlink-credentials-to-default", func(t *testing.T) {
		workDir := filepath.Join(home, "symlink-default")
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(workDir, ".credentials.json")
		if err := os.Symlink(defaultCreds, link); err != nil {
			t.Fatal(err)
		}
		path := writeAccountConfig(t, "providers:\n  claude:\n    enabled: true\n    accounts:\n      - id: work\n        config_dir: "+workDir+"\n")
		_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
		if !errors.Is(err, config.ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	})
}

func Test_Defaults_claude_credentials_path_empty_for_discovery(t *testing.T) {
	cfg := config.Defaults()
	if cfg.Providers["claude"].CredentialsPath != "" {
		t.Fatalf("Defaults Claude CredentialsPath = %q, want empty", cfg.Providers["claude"].CredentialsPath)
	}
}
