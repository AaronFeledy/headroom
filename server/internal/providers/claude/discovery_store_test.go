package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type fakeKeychain struct {
	data     []byte
	err      error
	services []string
}

func (k *fakeKeychain) Read(_ context.Context, service string) ([]byte, error) {
	k.services = append(k.services, service)
	return append([]byte(nil), k.data...), k.err
}

func Test_Client_discovery_selects_first_present_store(t *testing.T) {
	for _, tt := range []struct {
		name, goos, want  string
		keyErr            error
		explicit, account bool
	}{
		{"keychain first", "darwin", "keychain", nil, false, false},
		{"missing keychain", "darwin", "file", ErrCredentialsMissing, false, false},
		{"locked fallback", "darwin", "file", ErrKeychainLocked, false, false},
		{"unavailable fallback", "darwin", "file", ErrKeychainUnavailable, false, false},
		{"linux", "linux", "file", nil, false, false},
		{"windows", "windows", "file", nil, false, false},
		{"explicit", "darwin", "file", nil, true, false},
		{"account keychain", "darwin", "keychain", nil, false, true},
		{"account file", "darwin", "file", ErrCredentialsMissing, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			home := t.TempDir()
			dir := filepath.Join(home, ".claude")
			if tt.account {
				dir = filepath.Join(home, ".claude-work") + "/"
			}
			path := writeClaudeCredentialsIn(t, filepath.Join(dir, ".credentials.json"), credentialFixture{Access: "file", Refresh: "r", ExpiresAt: futureMillis(t)})
			keychain := &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "keychain", Refresh: "r", ExpiresAt: futureMillis(t)})), err: tt.keyErr}
			opts := Options{HomeDir: home, Keychain: keychain}
			if tt.explicit {
				opts.CredentialsPath = path
			}
			if tt.account {
				opts.ConfigDir = dir
				opts.Account = &usage.Account{ID: "work", Label: "Work"}
			}
			client := New(opts)
			client.goos = tt.goos
			// When
			got, err := client.loadCredentials(context.Background())
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if got.accessToken != tt.want {
				t.Fatalf("selected wrong store, want %s", tt.want)
			}
			if tt.goos != "darwin" || tt.explicit {
				if len(keychain.services) != 0 {
					t.Fatal("keychain read outside discovery contract")
				}
			} else {
				probes := 1
				if tt.account && errors.Is(tt.keyErr, ErrCredentialsMissing) {
					probes = 2
				}
				if len(keychain.services) != probes || keychain.services[0] != keychainService(opts.ConfigDir) {
					t.Fatal("wrong keychain service selected")
				}
			}
		})
	}
}

func Test_Client_keychain_fallback_recovers_on_next_fetch(t *testing.T) {
	// Given
	home := t.TempDir()
	writeClaudeCredentialsIn(t, filepath.Join(home, ".claude", ".credentials.json"), credentialFixture{Access: "file", Refresh: "r", ExpiresAt: futureMillis(t)})
	keychain := &fakeKeychain{err: ErrKeychainLocked}
	want := "file"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+want {
			t.Error("wrong credential selected after keychain unlock")
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, Keychain: keychain, UsageURL: server.URL, TokenURL: server.URL, HTTPClient: server.Client()})
	client.goos = "darwin"
	if got, err := client.Fetch(context.Background()); err != nil || got.Error != nil {
		t.Fatalf("fallback fetch failed: %v", err)
	}
	keychain.err = nil
	keychain.data = []byte(claudeCredentialsJSON(credentialFixture{Access: "keychain", Refresh: "r", ExpiresAt: futureMillis(t)}))
	want = "keychain"
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil || got.Auth.State != "signed_in" || requests != 2 {
		t.Fatalf("unlock fetch failed: %v", err)
	}
}

func Test_Client_keychain_errors_and_malformed_items_do_not_offer_wrong_fallback(t *testing.T) {
	for _, tt := range []struct {
		name    string
		keyErr  error
		data    string
		want    string
		command bool
	}{
		{"locked", ErrKeychainLocked, "", "The macOS Keychain is locked. Unlock it so Headroom can read your Claude Code sign-in.", false},
		{"unavailable", ErrKeychainUnavailable, "", "Headroom couldn't read your Claude Code sign-in from the macOS Keychain.", false},
		{"malformed", nil, "{", "Saved sign-in could not be read. Sign in again.", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			home := t.TempDir()
			if tt.name == "malformed" {
				writeClaudeCredentialsIn(t, filepath.Join(home, ".claude", ".credentials.json"), credentialFixture{Access: "file", Refresh: "r", ExpiresAt: futureMillis(t)})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("failed keychain resolution must not send an upstream request")
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			client := New(Options{HomeDir: home, Keychain: &fakeKeychain{data: []byte(tt.data), err: tt.keyErr}, HTTPClient: server.Client(), UsageURL: server.URL, TokenURL: server.URL})
			client.goos = "darwin"
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil || got.Error == nil || *got.Error != tt.want || got.Auth.State != "signed_out" {
				t.Fatalf("wrong sign-in row: %+v, %v", got, err)
			}
			if (got.Auth.SignInCommand != nil) != tt.command || (got.ReauthCommand != nil) != tt.command {
				t.Fatal("incorrect command availability")
			}
			if client.loaded != nil {
				t.Fatal("failed resolution retained credentials")
			}
		})
	}
}

func Test_Client_rediscovers_after_file_deletion_and_recreation(t *testing.T) {
	// Given
	home := t.TempDir()
	path := writeClaudeCredentialsIn(t, filepath.Join(home, ".claude", ".credentials.json"), credentialFixture{Access: "file", Refresh: "r", ExpiresAt: futureMillis(t)})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer recreated" {
			t.Error("recreated file credentials were not selected")
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, UsageURL: server.URL, HTTPClient: server.Client()})
	if _, err := client.loadCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil || got.Error == nil || *got.Error != "Not signed in." || got.Auth.State != "signed_out" || client.loaded != nil || requests != 0 {
		t.Fatalf("deleted file row: %+v, %v", got, err)
	}
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "recreated", Refresh: "r2", ExpiresAt: futureMillis(t)})
	got, err = client.Fetch(context.Background())
	if err != nil || got.Error != nil || got.Auth.State != "signed_in" || requests != 1 {
		t.Fatalf("recreated file did not recover: %v", err)
	}
}

func Test_Client_missing_explicit_file_does_not_discover_other_stores(t *testing.T) {
	// Given
	home := t.TempDir()
	writeClaudeCredentialsIn(t, filepath.Join(home, ".claude", ".credentials.json"), credentialFixture{Access: "native", Refresh: "r", ExpiresAt: futureMillis(t)})
	wslHome := t.TempDir()
	writeClaudeCredentialsIn(t, filepath.Join(wslHome, ".claude", ".credentials.json"), credentialFixture{Access: "wsl", Refresh: "r", ExpiresAt: futureMillis(t)})
	openCodePath := writeOpenCodeCredentialsIn(t, filepath.Join(home, "opencode", "auth.json"), credentialFixture{Access: "opencode", Refresh: "r", ExpiresAt: futureMillis(t)})
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			keychain := &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "keychain", Refresh: "r", ExpiresAt: futureMillis(t)}))}
			client := New(Options{CredentialsPath: filepath.Join(home, "missing.json"), HomeDir: home, WSLHomeDir: wslHome, OpenCodePath: openCodePath, Keychain: keychain})
			client.goos = goos
			// When
			got, err := client.loadCredentials(context.Background())
			// Then
			if !errors.Is(err, ErrCredentialsMissing) || got != nil || len(keychain.services) != 0 {
				t.Fatalf("missing explicit file used discovery: credentials=%v error=%v keychain reads=%d", got != nil, err, len(keychain.services))
			}
		})
	}
}

func Test_Client_failed_reload_clears_prior_credentials(t *testing.T) {
	// Given
	path := writeClaudeCredentials(t, credentialFixture{Access: "a", Refresh: "r", ExpiresAt: futureMillis(t)})
	client := New(Options{CredentialsPath: path})
	if _, err := client.loadCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("{"))
	// When
	_, err := client.loadCredentials(context.Background())
	// Then
	if !errors.Is(err, ErrCredentialsMalformed) || client.loaded != nil {
		t.Fatal("malformed reload retained prior credentials")
	}
}
