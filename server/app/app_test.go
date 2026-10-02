package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/cursor"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func Test_Run_rejects_conflicting_private_modes_before_provider_construction(t *testing.T) {
	err := runContext(context.Background(), []string{
		"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-desktop-session", "-ssh-access",
	}, []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}, discardLogger(), desktopSessionOptions{})
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func Test_Run_SSHStdio_is_exclusive_and_skips_config_loading(t *testing.T) {
	var output bytes.Buffer
	err := runContext(context.Background(), []string{"--ssh-stdio"}, []string{
		"USAGE_CONFIG=/definitely/not/read", "USAGE_PROVIDER_UNKNOWN_ENABLED=true",
	}, discardLogger(), desktopSessionOptions{input: bytes.NewBufferString("{}\n{}\n"), output: &output, homeDir: filepath.Join(t.TempDir(), "home")})
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatal("stdio mode accepted on unsupported platform")
		}
		return
	}
	if err != nil {
		t.Fatalf("stdio mode: %v", err)
	}
	if output.Len() == 0 {
		t.Fatal("stdio mode wrote no response")
	}
	if err := runContext(context.Background(), []string{"--ssh-stdio", "-config", "missing"}, nil, discardLogger(), desktopSessionOptions{}); err == nil {
		t.Fatal("combined ssh-stdio arguments were accepted")
	}
}

func Test_Run_rejects_off_loopback_empty_auth_before_provider_construction(t *testing.T) {
	// Given
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-listen-addr", "0.0.0.0:0", "-auth-token", ""}
	env := []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}

	// When
	err := Run(context.Background(), args, env, discardLogger(), "test-version",
		bytes.NewReader(nil), io.Discard)

	// Then
	if !errors.Is(err, api.ErrUnsafeBind) {
		t.Fatalf("run error = %v, want ErrUnsafeBind", err)
	}
}

func Test_Run_allows_loopback_empty_auth_until_later_startup_error(t *testing.T) {
	// Given
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-listen-addr", "127.0.0.1:0", "-auth-token", ""}
	env := []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}

	// When
	err := runContext(context.Background(), args, env, discardLogger(), desktopSessionOptions{})

	// Then
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("run error = %v, want ErrInvalidConfig", err)
	}
}

func Test_Run_allows_authenticated_off_loopback_until_later_startup_error(t *testing.T) {
	// Given
	args := []string{"-config", filepath.Join(t.TempDir(), "missing.yaml"), "-listen-addr", "0.0.0.0:0", "-auth-token", "secret"}
	env := []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}

	// When
	err := runContext(context.Background(), args, env, discardLogger(), desktopSessionOptions{})

	// Then
	if !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("run error = %v, want ErrInvalidConfig", err)
	}
}

func Test_BuildPoller_discovers_cursor_credentials_for_every_bind(t *testing.T) {
	// Given
	tests := []struct {
		name          string
		listenAddr    string
		wantDiscovery bool
	}{
		{name: "ipv4 loopback", listenAddr: "127.0.0.1:7823", wantDiscovery: true},
		{name: "localhost", listenAddr: "localhost:7823", wantDiscovery: true},
		{name: "ipv6 loopback", listenAddr: "[::1]:7823", wantDiscovery: true},
		{name: "wildcard ipv4", listenAddr: "0.0.0.0:7823"},
		{name: "wildcard ipv6", listenAddr: "[::]:7823"},
		{name: "lan address", listenAddr: "192.168.1.5:7823"},
		{name: "hostname", listenAddr: "example.com:7823"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("WSL_DISTRO_NAME", "")
			path := filepath.Join(home, "auth.json")
			if err := os.WriteFile(path, []byte(`{"accessToken":"header.eyJzdWIiOiJmaXh0dXJlIiwiZXhwIjoxfQ.sig"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults()
			cfg.ListenAddr = tt.listenAddr
			cfg.Providers = map[string]config.ProviderConfig{
				"cursor": {Enabled: true, CredentialsPath: path},
			}

			// When
			discovery := &cursor.Discovery{Environment: winprofile.Environment{GOOS: "linux", Home: home, Root: t.TempDir(), Env: func(string) string { return "" }}, Now: time.Now}
			_, _, cursorClient, _, _, err := buildPollerWithCursorDiscovery(cfg, discovery)
			if err != nil {
				t.Fatalf("buildPoller error = %v", err)
			}

			// Then
			if cursorClient == nil {
				t.Fatal("cursor client = nil")
			}
			data, err := cursorClient.Fetch(context.Background())
			if err != nil || data.Auth.State != "expired" || data.Auth.Source == nil || data.Auth.Source.Kind != "cli" {
				t.Fatalf("discovery failed on %s: %#v %v", tt.listenAddr, data.Auth, err)
			}
		})
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
