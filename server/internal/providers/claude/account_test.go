package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_account_identity_methods_and_reset_dir_invariant(t *testing.T) {
	// Given
	account := &usage.Account{ID: "work", Label: "Work"}
	resetDir := t.TempDir()
	client := New(Options{
		Name:            "Claude (Work)",
		Account:         account,
		ResetStateDir:   resetDir,
		CredentialsPath: filepath.Join(t.TempDir(), ".claude-work", ".credentials.json"),
	})

	// Then
	if client.Name() != "Claude (Work)" {
		t.Fatalf("Name = %q", client.Name())
	}
	if client.ProviderType() != "Claude" {
		t.Fatalf("ProviderType = %q", client.ProviderType())
	}
	got := client.Account()
	if got == nil || got.ID != "work" || got.Label != "Work" {
		t.Fatalf("Account = %+v", got)
	}
	if client.resetStateDir != "" {
		t.Fatalf("resetStateDir = %q, want empty for extra account", client.resetStateDir)
	}
	// Mutating returned account must not affect client
	got.ID = "mutated"
	if client.Account().ID != "work" {
		t.Fatal("Account() did not clone")
	}
}

func Test_Client_default_keeps_name_account_nil_command_and_reset_dir(t *testing.T) {
	resetDir := t.TempDir()
	client := New(Options{ResetStateDir: resetDir})
	if client.Name() != "Claude" || client.Account() != nil {
		t.Fatalf("name=%q account=%v", client.Name(), client.Account())
	}
	if client.signInCommand == nil || *client.signInCommand != reauthCommandText {
		t.Fatalf("signInCommand = %v", client.signInCommand)
	}
	if client.resetStateDir != filepath.Clean(resetDir) {
		t.Fatalf("resetStateDir = %q", client.resetStateDir)
	}
}

func Test_accountSignInCommand_shapes(t *testing.T) {
	tests := []struct {
		name string
		goos string
		dir  string
		want string
	}{
		{"posix plain", "linux", "/home/user/.claude-work", "CLAUDE_CONFIG_DIR=/home/user/.claude-work claude auth login"},
		{"posix space and quote", "linux", `/home/user/My Work's`, "CLAUDE_CONFIG_DIR='/home/user/My Work'\\''s' claude auth login"},
		{"windows quote", "windows", `C:\Users\O'Brien\.claude-work`, `$previous = $env:CLAUDE_CONFIG_DIR; $env:CLAUDE_CONFIG_DIR = 'C:\Users\O''Brien\.claude-work'; try { claude auth login } finally { $env:CLAUDE_CONFIG_DIR = $previous }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountSignInCommand(tt.goos, tt.dir)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func Test_Client_extra_account_nil_command_for_auth_json(t *testing.T) {
	client := New(Options{
		Name:            "Claude (Work)",
		Account:         &usage.Account{ID: "work", Label: "Work"},
		CredentialsPath: filepath.Join(t.TempDir(), "auth.json"),
	})
	if client.signInCommand != nil {
		t.Fatalf("signInCommand = %v, want nil for auth.json", *client.signInCommand)
	}
}

func Test_Client_extra_account_missing_path_does_not_fallback(t *testing.T) {
	// Given: default home credentials exist, extra path missing
	home := t.TempDir()
	writeClaudeCredentialsIn(t, filepath.Join(home, ".claude", ".credentials.json"), credentialFixture{
		Access: "home-access", Refresh: "home-refresh", ExpiresAt: futureMillis(t),
	})
	missing := filepath.Join(home, ".claude-work", ".credentials.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("missing account credentials must not cause an upstream request")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New(Options{
		Name:            "Claude (Work)",
		Account:         &usage.Account{ID: "work", Label: "Work"},
		CredentialsPath: missing,
		HomeDir:         home,
		HTTPClient:      server.Client(),
		UsageURL:        server.URL + "/api/oauth/usage",
		TokenURL:        server.URL + "/v1/oauth/token",
	})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ProviderName != "Claude (Work)" || got.ProviderType != "Claude" {
		t.Fatalf("identity name/type = %q/%q", got.ProviderName, got.ProviderType)
	}
	if got.Account == nil || got.Account.ID != "work" {
		t.Fatalf("account = %+v", got.Account)
	}
	if client.goos == "darwin" {
		if got.ReauthCommand != nil || got.Auth.SignInCommand != nil {
			t.Fatal("Keychain sign-in must not suggest a command")
		}
		return
	}
	prefix := "CLAUDE_CONFIG_DIR="
	if client.goos == "windows" {
		prefix = "$previous = $env:CLAUDE_CONFIG_DIR; $env:CLAUDE_CONFIG_DIR = '"
	}
	if got.ReauthCommand == nil || !strings.HasPrefix(*got.ReauthCommand, prefix) {
		t.Fatalf("reauth command = %v", got.ReauthCommand)
	}
	if !strings.Contains(*got.ReauthCommand, filepath.Dir(missing)) {
		t.Fatalf("command missing dir: %s", *got.ReauthCommand)
	}
	if got.Auth.SignInCommand == nil || *got.Auth.SignInCommand != *got.ReauthCommand {
		t.Fatalf("auth sign-in command = %v, want scoped reauth command", got.Auth.SignInCommand)
	}
}

func Test_Client_Fetch_stamps_extra_account_on_success(t *testing.T) {
	path := writeClaudeCredentials(t, credentialFixture{
		Access: "access-token", Refresh: "refresh-token", ExpiresAt: futureMillis(t), Subscription: "max",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(t, w, `{"five_hour":{"utilization":1},"seven_day":{"utilization":2}}`)
	}))
	defer server.Close()
	client := New(Options{
		Name:            "Claude (Work)",
		Account:         &usage.Account{ID: "work", Label: "Work"},
		CredentialsPath: path,
		HTTPClient:      server.Client(),
		UsageURL:        server.URL + "/api/oauth/usage",
		TokenURL:        server.URL + "/v1/oauth/token",
	})
	got, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderName != "Claude (Work)" || got.ProviderType != "Claude" {
		t.Fatalf("identity = %q/%q", got.ProviderName, got.ProviderType)
	}
	if got.Account == nil || got.Account.ID != "work" || got.Account.Label != "Work" {
		t.Fatalf("account = %+v", got.Account)
	}
	if got.Error != nil {
		t.Fatalf("error = %v", got.Error)
	}
}
