package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"gopkg.in/yaml.v3"
)

// The poller logs completion while holding its cache lock. A subsequent HTTP
// cache read waits for the completed entry to be published under that lock.
type accountFetchSignal struct{ completed chan struct{} }

func (s accountFetchSignal) Write(data []byte) (int, error) {
	var event struct {
		Message string `json:"msg"`
		Account string `json:"account"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	if event.Message == "provider fetch" && event.Account == "work" {
		select {
		case s.completed <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

func writeHTTPAccountConfig(t *testing.T, id, configDir string) string {
	t.Helper()
	// Marshal the path as a YAML scalar to preserve Windows separators and quotes.
	pathValue, err := yaml.Marshal(configDir)
	if err != nil {
		t.Fatal(err)
	}
	body := "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: " + id + "\n        label: Work\n        config_dir: " + string(pathValue)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func Test_Run_missing_claude_account_serves_actionable_sign_in_over_HTTP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	configDir := filepath.Join(home, "claude-work")
	path := writeHTTPAccountConfig(t, "work", configDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listenerReady := make(chan net.Listener, 1)
	result := make(chan error, 1)
	completed := make(chan struct{}, 1)
	logger := slog.New(slog.NewJSONHandler(accountFetchSignal{completed: completed}, nil))
	options := desktopSessionOptions{listen: func(network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			listenerReady <- listener
		}
		return listener, err
	}}
	go func() {
		result <- runContext(ctx, []string{"-config", path, "-listen-addr", "127.0.0.1:0"}, disabledProviderEnv(), logger, options)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("runContext: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	var listener net.Listener
	select {
	case listener = <-listenerReady:
	case <-ctx.Done():
		t.Fatal("listener was not created")
	}
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("first account fetch did not complete")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	base := "http://" + listener.Addr().String()
	get := func(route string, target any) {
		t.Helper()
		response, err := client.Get(base + route)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", route, response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	type accountRow struct {
		ProviderName string         `json:"provider_name"`
		ProviderType string         `json:"provider_type"`
		Account      *usage.Account `json:"account"`
		NeedsReauth  bool           `json:"needs_reauth"`
		Error        *string        `json:"error"`
		Reauth       *string        `json:"reauth_command"`
		Success      bool           `json:"is_success"`
		Auth         usage.Auth     `json:"auth"`
		FetchStatus  struct {
			FailureKind *string `json:"failure_kind"`
		} `json:"fetch_status"`
	}
	var rows []accountRow
	get("/api/v1/usage", &rows)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.ProviderName != "Claude (Work)" || row.ProviderType != "Claude" || !reflect.DeepEqual(row.Account, &usage.Account{ID: "work", Label: "Work"}) {
		t.Fatalf("account identity = %+v", row)
	}
	wantMessage := "Not signed in."
	command := "CLAUDE_CONFIG_DIR=" + configDir + " claude auth login"
	if runtime.GOOS == "windows" {
		command = "$previous = $env:CLAUDE_CONFIG_DIR; $env:CLAUDE_CONFIG_DIR = '" + strings.ReplaceAll(configDir, "'", "''") + "'; try { claude auth login } finally { $env:CLAUDE_CONFIG_DIR = $previous }"
	} else if strings.ContainsAny(configDir, " '\t") {
		command = "CLAUDE_CONFIG_DIR='" + strings.ReplaceAll(configDir, "'", `'\''`) + "' claude auth login"
	}
	wantCommand := &command
	if row.Error == nil || *row.Error != wantMessage || !row.NeedsReauth || row.Success || row.Auth.State != "signed_out" {
		t.Fatalf("sign-in row = %+v, want %q", row, wantMessage)
	}
	if !reflect.DeepEqual(row.Auth.SignInCommand, wantCommand) || !reflect.DeepEqual(row.Reauth, wantCommand) {
		t.Fatalf("commands = %v/%v, want %v", row.Auth.SignInCommand, row.Reauth, wantCommand)
	}
	if row.FetchStatus.FailureKind == nil || *row.FetchStatus.FailureKind != "auth" {
		t.Fatalf("failure kind = %v, want auth", row.FetchStatus.FailureKind)
	}
	var single accountRow
	get("/api/v1/usage/Claude%20(Work)", &single)
	if !reflect.DeepEqual(single, row) {
		t.Fatalf("single account differs: %+v", single)
	}
	var health struct {
		Providers []struct{ Name string } `json:"providers"`
	}
	get("/api/v1/health", &health)
	if len(health.Providers) != 1 || health.Providers[0].Name != "Claude (Work)" {
		t.Fatalf("health providers = %+v", health.Providers)
	}
}

func Test_Run_invalid_account_rejected_before_listen(t *testing.T) {
	path := writeHTTPAccountConfig(t, "Bad ID", filepath.Join(t.TempDir(), "work"))
	listened := false
	err := runContext(context.Background(), []string{"-config", path, "-listen-addr", "127.0.0.1:0"}, disabledProviderEnv(), discardLogger(), desktopSessionOptions{
		listen: func(network, address string) (net.Listener, error) {
			listened = true
			return nil, errors.New("unexpected listener creation")
		},
	})
	if !errors.Is(err, config.ErrInvalidConfig) || listened {
		t.Fatalf("error=%v listened=%v, want ErrInvalidConfig before listen", err, listened)
	}
}
