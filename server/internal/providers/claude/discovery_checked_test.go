package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_checked_records_examined_sources(t *testing.T) {
	for _, tt := range []struct {
		name, goos, keyData, fileData    string
		keyErr                           error
		explicit, wsl, openCode, account bool
		want                             []usage.AuthChecked
	}{
		{name: "keychain selected", goos: "darwin", keyData: "valid", want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "signed_in"}}},
		{name: "missing keychain then file", goos: "darwin", keyErr: ErrCredentialsMissing, fileData: "valid", want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "Claude Code", Status: "signed_in"}}},
		{name: "locked without fallback", goos: "darwin", keyErr: ErrKeychainLocked, want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "locked"}, {Kind: "cli", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "OpenCode", Status: "signed_out"}}},
		{name: "unavailable without fallback", goos: "darwin", keyErr: ErrKeychainUnavailable, want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "unreadable"}, {Kind: "cli", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "OpenCode", Status: "signed_out"}}},
		{name: "malformed keychain stops", goos: "darwin", keyData: "{", fileData: "valid", want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "unreadable"}}},
		{name: "explicit file", goos: "darwin", explicit: true, fileData: "valid", want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_in"}}},
		{name: "explicit missing", goos: "darwin", explicit: true, want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_out"}}},
		{name: "malformed file", goos: "linux", fileData: "{", want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "unreadable"}}},
		{name: "file read error", goos: "linux", fileData: "directory", want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "unreadable"}}},
		{name: "account file", goos: "darwin", account: true, keyErr: ErrCredentialsMissing, fileData: "valid", want: []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "Claude Code", Status: "signed_in"}}},
		{name: "linux file", goos: "linux", fileData: "valid", want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_in"}}},
		{name: "windows file", goos: "windows", fileData: "valid", want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_in"}}},
		{name: "windows WSL", goos: "windows", wsl: true, want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "Claude Code (WSL)", Status: "signed_in"}}},
		{name: "OpenCode", goos: "linux", openCode: true, want: []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "signed_out"}, {Kind: "cli", Name: "OpenCode", Status: "signed_in"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given: isolated files and a fake Keychain; HTTP is local only.
			home, wslHome := t.TempDir(), t.TempDir()
			valid := claudeCredentialsJSON(credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: 4102444800000})
			keyData := tt.keyData
			if keyData == "valid" {
				keyData = valid
			}
			keychain := &fakeKeychain{data: []byte(keyData), err: tt.keyErr}
			path := filepath.Join(home, ".claude", ".credentials.json")
			if tt.account {
				path = filepath.Join(home, "work", ".credentials.json")
			}
			if tt.fileData == "directory" {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if tt.fileData != "" {
				data := tt.fileData
				if data == "valid" {
					data = valid
				}
				writeFile(t, path, []byte(data))
			}
			if tt.wsl {
				writeFile(t, filepath.Join(wslHome, ".claude", ".credentials.json"), []byte(valid))
			}
			openCodePath := filepath.Join(home, "opencode", "auth.json")
			if tt.openCode {
				writeOpenCodeCredentialsIn(t, openCodePath, credentialFixture{Access: "opencode", Refresh: "r", ExpiresAt: 4102444800000})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeResponse(t, w, `{"five_hour":{"utilization":1}}`) }))
			defer server.Close()
			opts := Options{HomeDir: home, WSLHomeDir: wslHome, OpenCodePath: openCodePath, Keychain: keychain, UsageURL: server.URL, TokenURL: server.URL, HTTPClient: server.Client()}
			if tt.explicit {
				opts.CredentialsPath = path
			}
			if tt.account {
				opts.ConfigDir = filepath.Dir(path)
				opts.Account = &usage.Account{ID: "work", Label: "Work"}
			}
			client := New(opts)
			client.goos = tt.goos
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Auth.Checked, tt.want) {
				t.Fatalf("checked = %+v, want %+v", got.Auth.Checked, tt.want)
			}
			if (tt.goos != "darwin" || tt.explicit) && len(keychain.services) != 0 {
				t.Fatal("unexpected Keychain probe")
			}
			if got.Error != nil && client.loaded != nil {
				t.Fatal("failed discovery retained credentials")
			}
		})
	}
}

func Test_Client_failed_discovery_replaces_checked_without_loaded_credentials(t *testing.T) {
	// Given
	path := writeClaudeCredentials(t, credentialFixture{Access: "a", Refresh: "r", ExpiresAt: 4102444800000})
	client := New(Options{CredentialsPath: path})
	if _, err := client.loadCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("{"))
	// When
	_, err := client.loadCredentials(context.Background())
	// Then
	want := []usage.AuthChecked{{Kind: "cli", Name: "Claude Code", Status: "unreadable"}}
	if !errors.Is(err, ErrCredentialsMalformed) || client.loaded != nil || !reflect.DeepEqual(client.checked, want) {
		t.Fatalf("failed discovery did not retain its checked result: %v", err)
	}
}
