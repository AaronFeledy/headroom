package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_sign_in_override_is_verbatim_and_extra_account_only(t *testing.T) {
	for _, extra := range []bool{false, true} {
		opts := Options{HomeDir: t.TempDir(), CredentialsPath: filepath.Join(t.TempDir(), "missing"), SignInCommand: " custom login command "}
		want := "claude auth login"
		if extra {
			opts.Account = &usage.Account{ID: "work", Label: "Work"}
			want = opts.SignInCommand
		}
		result, err := New(opts).Fetch(context.Background())
		if err != nil || result.Auth.SignInCommand == nil || *result.Auth.SignInCommand != want || result.ReauthCommand == nil || *result.ReauthCommand != want {
			t.Fatalf("extra=%v result=%+v error=%v", extra, result, err)
		}
	}
}

func Test_Client_expired_keychain_uses_sign_in_override(t *testing.T) {
	command := "headroom accounts login claude work"
	client := New(Options{
		ConfigDir: filepath.Join(t.TempDir(), "work"),
		Account:   &usage.Account{ID: "work", Label: "Work"}, SignInCommand: command,
		Keychain: &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: 1}))},
	})
	client.goos = "darwin"
	result, err := client.Fetch(context.Background())
	if err != nil || result.Error == nil || !strings.Contains(*result.Error, command) || result.Auth.SignInCommand == nil || *result.Auth.SignInCommand != command {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
