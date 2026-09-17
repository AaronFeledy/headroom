package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test_Client_loadCredentials_discovers_configured_native_wsl_then_opencode(t *testing.T) {
	// Given
	root := t.TempDir()
	overridePath := writeClaudeCredentialsIn(t, filepath.Join(root, "override.json"), credentialFixture{Access: "override", Refresh: "r", ExpiresAt: futureMillis(t)})
	nativeHome := filepath.Join(root, "native")
	nativePath := writeClaudeCredentialsIn(t, filepath.Join(nativeHome, ".claude", ".credentials.json"), credentialFixture{Access: "native", Refresh: "r", ExpiresAt: futureMillis(t)})
	wslHome := filepath.Join(root, "wsl")
	wslPath := writeClaudeCredentialsIn(t, filepath.Join(wslHome, ".claude", ".credentials.json"), credentialFixture{Access: "wsl", Refresh: "r", ExpiresAt: futureMillis(t)})
	openCodePath := writeOpenCodeCredentialsIn(t, filepath.Join(root, "opencode", "auth.json"), credentialFixture{Access: "opencode", Refresh: "r", ExpiresAt: futureMillis(t)})

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{name: "configured override", opts: Options{CredentialsPath: overridePath, HomeDir: nativeHome, WSLHomeDir: wslHome, OpenCodePath: openCodePath}, want: "override"},
		{name: "native claude", opts: Options{HomeDir: nativeHome, WSLHomeDir: wslHome, OpenCodePath: openCodePath}, want: "native"},
		{name: "wsl claude", opts: Options{HomeDir: filepath.Join(root, "missing-native"), WSLHomeDir: wslHome, OpenCodePath: openCodePath}, want: "wsl"},
		{name: "opencode", opts: Options{HomeDir: filepath.Join(root, "missing-native"), WSLHomeDir: filepath.Join(root, "missing-wsl"), OpenCodePath: openCodePath}, want: "opencode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got, err := New(tt.opts).loadCredentials(context.Background())

			// Then
			if err != nil {
				t.Fatalf("loadCredentials returned error: %v", err)
			}
			if got.accessToken != tt.want {
				t.Fatalf("accessToken = %q, want %q", got.accessToken, tt.want)
			}
		})
	}
	if nativePath == "" || wslPath == "" {
		t.Fatalf("fixture paths were not created")
	}
}

func Test_Client_loadCredentials_parses_anthropic_shape(t *testing.T) {
	// Given
	path := writeOpenCodeCredentials(t, credentialFixture{Access: "access", Refresh: "refresh", ExpiresAt: futureMillis(t), Extra: `,"unknown":true`})

	// When
	got, err := New(Options{CredentialsPath: path}).loadCredentials(context.Background())

	// Then
	if err != nil {
		t.Fatalf("loadCredentials returned error: %v", err)
	}
	if got.accessToken != "access" || got.refreshToken != "refresh" || got.source != credentialSourceOpenCode {
		t.Fatalf("credentials = %#v", got)
	}
}

func Test_Client_Fetch_returns_typed_missing_credentials_error(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing.json")
	client := New(Options{CredentialsPath: path})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("error = %v, want ErrCredentialsMissing", err)
	}
	if !got.NeedsReauth || got.ReauthCommand == nil || *got.ReauthCommand != "claude auth login" {
		t.Fatalf("usage = %#v", got)
	}
}

func Test_Client_Fetch_returns_malformed_error_for_invalid_json(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":`), 0o600); err != nil {
		t.Fatalf("write malformed credentials: %v", err)
	}
	client := New(Options{CredentialsPath: path})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if !errors.Is(err, ErrCredentialsMalformed) {
		t.Fatalf("error = %v, want ErrCredentialsMalformed", err)
	}
	if got.Error == nil || got.ReauthCommand == nil {
		t.Fatalf("usage = %#v", got)
	}
}
