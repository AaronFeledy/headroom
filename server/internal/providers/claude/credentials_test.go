package claude

import (
	"context"
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
			client := New(tt.opts)
			client.goos = "windows"
			got, err := client.loadCredentials(context.Background())

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
