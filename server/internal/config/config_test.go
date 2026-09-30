package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_returns_defaults_when_config_file_missing(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing.yaml")

	// When
	cfg, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}})

	// Then
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7823" {
		t.Fatalf("ListenAddr = %q, want default", cfg.ListenAddr)
	}
	if cfg.AuthToken != "" {
		t.Fatalf("AuthToken = %q, want empty", cfg.AuthToken)
	}
	if cfg.DesktopSession {
		t.Fatal("DesktopSession = true by default")
	}
	if cfg.PollInterval != 60*time.Second {
		t.Fatalf("PollInterval = %s, want 60s", cfg.PollInterval)
	}
	for _, name := range []string{"claude", "codex", "cursor", "grok"} {
		if got := cfg.Providers[name].Enabled; !got {
			t.Fatalf("%s provider enabled = %v, want true", name, got)
		}
	}
}

func Test_Load_enables_desktop_session_only_from_CLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("desktop_session: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	fromFileAndEnv, err := config.Load(context.Background(), config.LoadOptions{
		Args: []string{"-config", path}, Env: []string{"USAGE_DESKTOP_SESSION=true"},
	})
	if err != nil {
		t.Fatalf("Load file/env: %v", err)
	}
	if fromFileAndEnv.DesktopSession {
		t.Fatal("desktop session was enabled outside the CLI")
	}
	fromCLI, err := config.Load(context.Background(), config.LoadOptions{
		Args: []string{"-config", path, "-desktop-session"}, Env: []string{"USAGE_DESKTOP_SESSION=false"},
	})
	if err != nil {
		t.Fatalf("Load CLI: %v", err)
	}
	if !fromCLI.DesktopSession {
		t.Fatal("desktop session CLI flag was ignored")
	}
}

func Test_Load_applies_precedence_flags_over_env_over_yaml_over_defaults(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := []byte("listen_addr: 127.0.0.1:7000\nauth_token: from-yaml\npoll_interval: 30s\nproviders:\n  claude:\n    enabled: false\n    credentials_path: /yaml/claude.json\n")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := []string{
		"USAGE_LISTEN_ADDR=127.0.0.1:7001",
		"USAGE_AUTH_TOKEN=from-env",
		"USAGE_POLL_INTERVAL=45s",
		"USAGE_PROVIDER_CLAUDE_ENABLED=true",
		"USAGE_PROVIDER_CLAUDE_CREDENTIALS_PATH=/env/claude.json",
	}
	args := []string{"-config", path, "-listen-addr", "127.0.0.1:7002", "-auth-token", "from-flag", "-poll-interval", "90s"}

	// When
	cfg, err := config.Load(context.Background(), config.LoadOptions{Args: args, Env: env})

	// Then
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7002" {
		t.Fatalf("ListenAddr = %q, want flag", cfg.ListenAddr)
	}
	if cfg.AuthToken != "from-flag" {
		t.Fatalf("AuthToken = %q, want flag", cfg.AuthToken)
	}
	if cfg.PollInterval != 90*time.Second {
		t.Fatalf("PollInterval = %s, want flag", cfg.PollInterval)
	}
	provider := cfg.Providers["claude"]
	if !provider.Enabled {
		t.Fatalf("claude provider enabled = false, want env true")
	}
	if provider.CredentialsPath != "/env/claude.json" {
		t.Fatalf("CredentialsPath = %q, want env", provider.CredentialsPath)
	}
}

func Test_Load_applies_SSH_access_precedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("ssh_access: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromYAML, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}})
	if err != nil || !fromYAML.SSHAccess {
		t.Fatalf("YAML SSHAccess = %v, err=%v", fromYAML.SSHAccess, err)
	}
	fromEnv, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Env: []string{"USAGE_SSH_ACCESS=false"}})
	if err != nil || fromEnv.SSHAccess {
		t.Fatalf("env SSHAccess = %v, err=%v", fromEnv.SSHAccess, err)
	}
	fromFlag, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path, "-ssh-access=false"}, Env: []string{"USAGE_SSH_ACCESS=true"}})
	if err != nil || fromFlag.SSHAccess {
		t.Fatalf("flag SSHAccess = %v, err=%v", fromFlag.SSHAccess, err)
	}
}

func Test_Load_rejects_invalid_SSH_access_env(t *testing.T) {
	_, err := config.Load(context.Background(), config.LoadOptions{
		Args: []string{"-config", filepath.Join(t.TempDir(), "missing.yaml")}, Env: []string{"USAGE_SSH_ACCESS=maybe"},
	})
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("Load error = %v, want ErrInvalidConfig", err)
	}
}

func Test_Load_does_not_cache_values_between_calls(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing.yaml")
	firstEnv := []string{"USAGE_LISTEN_ADDR=127.0.0.1:7100"}
	secondEnv := []string{"USAGE_LISTEN_ADDR=127.0.0.1:7200"}

	// When
	first, firstErr := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Env: firstEnv})
	second, secondErr := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Env: secondEnv})

	// Then
	if firstErr != nil {
		t.Fatalf("first Load returned error: %v", firstErr)
	}
	if secondErr != nil {
		t.Fatalf("second Load returned error: %v", secondErr)
	}
	if first.ListenAddr != "127.0.0.1:7100" {
		t.Fatalf("first ListenAddr = %q", first.ListenAddr)
	}
	if second.ListenAddr != "127.0.0.1:7200" {
		t.Fatalf("second ListenAddr = %q", second.ListenAddr)
	}
}

func Test_Load_returns_malformed_config_error_when_yaml_invalid(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  claude: ["), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// When
	_, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}})

	// Then
	if !errors.Is(err, config.ErrMalformedConfig) {
		t.Fatalf("Load error = %v, want ErrMalformedConfig", err)
	}
}

func Test_Load_returns_invalid_config_error_when_duration_invalid(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("poll_interval: nope\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// When
	_, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}})

	// Then
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("Load error = %v, want ErrInvalidConfig", err)
	}
}

func Test_Load_returns_invalid_config_error_when_provider_enabled_env_invalid(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing.yaml")
	env := []string{"USAGE_PROVIDER_CLAUDE_ENABLED=maybe"}

	// When
	_, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}, Env: env})

	// Then
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("Load error = %v, want ErrInvalidConfig", err)
	}
}

func Test_DefaultPath_returns_os_config_path(t *testing.T) {
	// Given
	base := t.TempDir()
	env := []string{"XDG_CONFIG_HOME=" + base, "APPDATA=" + base, "HOME=" + base}

	// When
	linuxPath, linuxErr := config.DefaultPath("linux", env)
	windowsPath, windowsErr := config.DefaultPath("windows", env)

	// Then
	if linuxErr != nil {
		t.Fatalf("DefaultPath linux returned error: %v", linuxErr)
	}
	if linuxPath != filepath.Join(base, "headroom", "config.yaml") {
		t.Fatalf("linux path = %q", linuxPath)
	}
	if windowsErr != nil {
		t.Fatalf("DefaultPath windows returned error: %v", windowsErr)
	}
	if windowsPath != filepath.Join(base, "Headroom", "config.yaml") {
		t.Fatalf("windows path = %q", windowsPath)
	}
}
