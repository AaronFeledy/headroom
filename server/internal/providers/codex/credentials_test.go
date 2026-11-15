package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_Credentials_loads_codex_api_key_without_refresh_when_OPENAI_API_KEY_present(t *testing.T) {
	// Given
	path := writeAuth(t, `{"OPENAI_API_KEY":"sk-test","tokens":{"access_token":"ignored","refresh_token":"ignored","account_id":"acct"}}`)
	store := NewCredentialStore(CredentialOptions{Path: path})

	// When
	creds, err := store.Load(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if creds.AccessToken != "sk-test" || creds.RefreshToken != "" || creds.AccountID != "" {
		t.Fatalf("credentials = %+v, want api-key only", creds)
	}
	if store.NeedsRefresh(time.Now()) {
		t.Fatal("NeedsRefresh = true, want false for OPENAI_API_KEY")
	}
}

func Test_Credentials_loads_codex_tokens_and_preserves_unknown_fields_on_writeback(t *testing.T) {
	// Given
	path := writeAuth(t, `{"sentinel":"keep","tokens":{"access_token":"old","refresh_token":"old-refresh","account_id":"acct","token_sentinel":"keep"},"last_refresh":"2026-07-01T00:00:00Z"}`)
	store := NewCredentialStore(CredentialOptions{Path: path})

	// When
	creds, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	err = store.Refresh(context.Background(), creds, func(Credentials) (RefreshedTokens, error) {
		return RefreshedTokens{AccessToken: "new", RefreshToken: "new-refresh", AccountID: creds.AccountID, ExpiresAt: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), RefreshedAt: time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	got := string(readFile(t, path))
	for _, want := range []string{`"sentinel":"keep"`, `"token_sentinel":"keep"`, `"access_token":"new"`, `"refresh_token":"new-refresh"`, `"account_id":"acct"`, `"last_refresh":"2026-07-12T11:00:00Z"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated auth missing %s in %s", want, got)
		}
	}
}

func Test_Credentials_loads_opencode_tokens_and_preserves_unknown_fields_on_writeback(t *testing.T) {
	// Given
	path := writeAuth(t, `{"other":"keep","openai":{"access":"old","refresh":"old-refresh","accountId":"acct","expires":1783867200000,"nested":"keep"}}`)
	store := NewCredentialStore(CredentialOptions{Path: path, Source: CredentialSourceOpenCode})

	// When
	creds, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	err = store.Refresh(context.Background(), creds, func(Credentials) (RefreshedTokens, error) {
		return RefreshedTokens{AccessToken: "new", RefreshToken: "new-refresh", AccountID: creds.AccountID, ExpiresAt: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), RefreshedAt: time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	got := string(readFile(t, path))
	for _, want := range []string{`"other":"keep"`, `"nested":"keep"`, `"access":"new"`, `"refresh":"new-refresh"`, `"accountId":"acct"`, `"expires":1783857600000`} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated opencode auth missing %s in %s", want, got)
		}
	}
}

func Test_Credentials_returns_typed_error_when_auth_path_missing_or_malformed(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		wantErr error
	}{
		{name: "missing path", path: filepath.Join(t.TempDir(), "missing.json"), wantErr: ErrCredentialsMissing},
		{name: "malformed json", content: `{"tokens":`, wantErr: ErrCredentialsMalformed},
		{name: "missing tokens", content: `{"sentinel":"keep"}`, wantErr: ErrCredentialsMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			path := tt.path
			if tt.content != "" {
				path = writeAuth(t, tt.content)
			}
			store := NewCredentialStore(CredentialOptions{Path: path})

			// When
			_, err := store.Load(context.Background())

			// Then
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Load error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func Test_DiscoverAuthPath_prefers_override_CODEX_HOME_home_wsl_then_opencode(t *testing.T) {
	// Given
	home := t.TempDir()
	codeHome := t.TempDir()
	override := filepath.Join(t.TempDir(), "override.json")
	wsl := filepath.Join(t.TempDir(), "wsl-auth.json")
	opencode := filepath.Join(t.TempDir(), "opencode-auth.json")
	for _, path := range []string{override, filepath.Join(codeHome, "auth.json"), filepath.Join(home, ".codex", "auth.json"), wsl, opencode} {
		writeFile(t, path, `{}`)
	}

	// When / Then
	assertDiscovered(t, DiscoveryOptions{ConfiguredPath: override, HomeDir: home, Env: map[string]string{"CODEX_HOME": codeHome}, WSLAuthPath: func(context.Context) (string, error) { return wsl, nil }, OpenCodeAuthPath: opencode}, override, CredentialSourceCodex)
	assertDiscovered(t, DiscoveryOptions{HomeDir: home, Env: map[string]string{"CODEX_HOME": codeHome}, WSLAuthPath: func(context.Context) (string, error) { return wsl, nil }, OpenCodeAuthPath: opencode}, filepath.Join(codeHome, "auth.json"), CredentialSourceCodex)
	if err := os.Remove(filepath.Join(codeHome, "auth.json")); err != nil {
		t.Fatalf("remove codex home auth: %v", err)
	}
	assertDiscovered(t, DiscoveryOptions{HomeDir: home, Env: map[string]string{"CODEX_HOME": codeHome}, WSLAuthPath: func(context.Context) (string, error) { return wsl, nil }, OpenCodeAuthPath: opencode}, filepath.Join(home, ".codex", "auth.json"), CredentialSourceCodex)
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatalf("remove home auth: %v", err)
	}
	assertDiscovered(t, DiscoveryOptions{HomeDir: home, Env: map[string]string{"CODEX_HOME": codeHome}, WSLAuthPath: func(context.Context) (string, error) { return wsl, nil }, OpenCodeAuthPath: opencode}, wsl, CredentialSourceCodex)
	if err := os.Remove(wsl); err != nil {
		t.Fatalf("remove wsl auth: %v", err)
	}
	assertDiscovered(t, DiscoveryOptions{HomeDir: home, Env: map[string]string{"CODEX_HOME": codeHome}, WSLAuthPath: func(context.Context) (string, error) { return wsl, nil }, OpenCodeAuthPath: opencode}, opencode, CredentialSourceOpenCode)
}

func assertDiscovered(t *testing.T, opts DiscoveryOptions, wantPath string, wantSource CredentialSource) {
	t.Helper()
	got, err := DiscoverAuthPath(context.Background(), opts)
	if err != nil {
		t.Fatalf("DiscoverAuthPath returned error: %v", err)
	}
	if got.Path != wantPath || got.Source != wantSource {
		t.Fatalf("DiscoverAuthPath = %+v, want path %q source %v", got, wantPath, wantSource)
	}
}

func writeAuth(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, path, content)
	return path
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
