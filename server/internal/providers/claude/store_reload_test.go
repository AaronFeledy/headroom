package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func Test_Client_freshness_failure_has_initial_sign_in_row(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		name := "malformed"
		if unreadable {
			name = "unreadable"
		}
		t.Run(name, func(t *testing.T) {
			// Given
			path := writeClaudeCredentials(t, credentialFixture{Access: "a", Refresh: "r", ExpiresAt: futureMillis(t)})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("failed freshness resolution must not request usage or refresh")
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			client := New(Options{CredentialsPath: path, HTTPClient: server.Client(), UsageURL: server.URL, TokenURL: server.URL})
			if _, err := client.loadCredentials(context.Background()); err != nil {
				t.Fatal(err)
			}
			if unreadable {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, path, []byte("{"))
			}
			changed := time.Unix(100, 0)
			if err := os.Chtimes(path, changed, changed); err != nil {
				t.Fatal(err)
			}
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil || got.Error == nil || *got.Error != "Saved sign-in could not be read. Sign in again." || got.Auth.State != "signed_out" || got.ReauthCommand == nil || got.Auth.SignInCommand == nil || client.loaded != nil {
				t.Fatalf("wrong freshness row: %+v, %v", got, err)
			}
		})
	}
}

func Test_Client_keychain_rotation_reloads_without_file_metadata(t *testing.T) {
	// Given
	keychain := &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "old", Refresh: "r", ExpiresAt: futureMillis(t)}))}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer rotated" {
			t.Error("keychain rotation was not loaded")
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: t.TempDir(), Keychain: keychain, HTTPClient: server.Client(), UsageURL: server.URL, TokenURL: server.URL})
	client.goos = "darwin"
	if _, err := client.loadCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	keychain.data = []byte(claudeCredentialsJSON(credentialFixture{Access: "rotated", Refresh: "r2", ExpiresAt: futureMillis(t)}))
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil || requests != 1 {
		t.Fatalf("rotation fetch failed: %+v, %v", got, err)
	}
}
