package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_keychain_expired_or_unauthorized_never_refreshes(t *testing.T) {
	for _, expired := range []bool{true, false} {
		for _, account := range []bool{false, true} {
			name := "unauthorized"
			if expired {
				name = "expired"
			}
			if account {
				name += " account"
			}
			t.Run(name, func(t *testing.T) {
				// Given
				expires := int64(4102444800000)
				if expired {
					expires = 1
				}
				keychain := &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "a", Refresh: "r", ExpiresAt: expires}))}
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if expired || r.URL.Path != "/usage" || r.Method != http.MethodGet {
						t.Error("read-only keychain sign-in must not refresh")
					}
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer server.Close()
				opts := Options{HomeDir: t.TempDir(), Keychain: keychain, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()}
				if account {
					opts.ConfigDir = filepath.Join(opts.HomeDir, "work") + "/"
					opts.Account = &usage.Account{ID: "work", Label: "Work"}
				}
				client := New(opts)
				client.goos = "darwin"
				// When
				got, err := client.Fetch(context.Background())
				// Then
				if err != nil || got.Error == nil || got.Auth.State != "expired" || !got.NeedsReauth {
					t.Fatalf("expected expired row: %+v, %v", got, err)
				}
				if got.Auth.Source == nil || got.Auth.Source.Kind != "cli" || got.Auth.Source.Name != "Claude Code" || got.Auth.SignInCommand == nil || got.ReauthCommand == nil || *got.Auth.SignInCommand != *client.signInCommand || *got.ReauthCommand != *client.signInCommand {
					t.Fatal("expired Keychain row must name Claude Code and retain its sign-in command")
				}
				if !reflect.DeepEqual(got.Auth.Checked, []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "expired"}}) {
					t.Fatalf("checked = %+v", got.Auth.Checked)
				}
				if !reflect.DeepEqual(client.checked, []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: "signed_in"}}) {
					t.Fatal("auth attachment mutated discovery results")
				}
				want := "Claude Code refreshes this sign-in when it runs. Use Claude Code once, or run `" + *client.signInCommand + "`."
				if *got.Error != want {
					t.Fatalf("error=%q", *got.Error)
				}
				if account && !strings.Contains(*got.Error, strings.ReplaceAll(opts.ConfigDir, "'", "''")) {
					t.Fatal("Keychain explanation must identify the extra account's config directory")
				}
				if expired && requests != 0 || !expired && requests != 1 {
					t.Fatalf("unexpected upstream requests=%d", requests)
				}
			})
		}
	}
}

func Test_Client_file_refresh_preserves_concurrent_sign_in(t *testing.T) {
	for _, openCode := range []bool{false, true} {
		name := "claude"
		if openCode {
			name = "opencode"
		}
		t.Run(name, func(t *testing.T) {
			// Given
			path := filepath.Join(t.TempDir(), "credentials.json")
			old := credentialFixture{Access: "old", Refresh: "old-refresh", ExpiresAt: pastMillis(t)}
			if openCode {
				writeOpenCodeCredentialsIn(t, path, old)
			} else {
				writeClaudeCredentialsAt(t, path, old)
			}
			concurrent := credentialFixture{Access: "other-account", Refresh: "other-refresh", ExpiresAt: futureMillis(t)}
			var original string
			usageRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					if openCode {
						writeOpenCodeCredentialsIn(t, path, concurrent)
					} else {
						writeClaudeCredentialsAt(t, path, concurrent)
					}
					original = readFile(t, path)
					writeResponse(t, w, `{"access_token":"refreshed-old","refresh_token":"refreshed-old-refresh","expires_in":3600}`)
					return
				}
				usageRequests++
				if r.URL.Path != "/usage" || r.Header.Get("Authorization") != "Bearer other-account" {
					t.Error("must reload the concurrent sign-in instead of using the refresh response")
				}
				writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
			}))
			defer server.Close()
			client := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil || got.Auth.State != "signed_in" || usageRequests != 1 {
				t.Fatalf("concurrent sign-in failed: %v", err)
			}
			if original == "" || readFile(t, path) != original || strings.Contains(original, "refreshed-old") {
				t.Fatal("refresh overwrote the concurrent sign-in")
			}
		})
	}
}
