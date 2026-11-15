package claude

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type probeTransport struct{ requests int }

func (p *probeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.requests++
	return nil, errors.New("probe attempted HTTP")
}

func Test_ProbeSignIn_reads_without_HTTP_or_credential_changes(t *testing.T) {
	for _, tt := range []struct {
		name, goos, state string
		expiry            int64
		keyErr            error
	}{
		{"refreshable expired file", "linux", "signed_in", 1, nil},
		{"expired keychain wins over file", "darwin", "expired", 1, nil},
		{"valid keychain", "darwin", "signed_in", 4102444800000, nil},
		{"locked keychain falls through to file", "darwin", "signed_in", 1, ErrKeychainLocked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "work")
			path := filepath.Join(dir, ".credentials.json")
			data := []byte(claudeCredentialsJSON(credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: tt.expiry}))
			writeFile(t, path, data)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			transport := &probeTransport{}
			keychain := &fakeKeychain{data: data, err: tt.keyErr}
			client := New(Options{HomeDir: home, ConfigDir: dir, Keychain: keychain, HTTPClient: &http.Client{Transport: transport}})
			client.goos = tt.goos
			got := client.ProbeSignIn(context.Background())
			if got.State != tt.state || got.Source == nil {
				t.Fatalf("probe=%+v", got)
			}
			wantKind := "cli"
			if tt.goos == "darwin" && tt.keyErr == nil {
				wantKind = "keychain"
			}
			if got.Source.Kind != wantKind || got.Source.Name != "Claude Code" {
				t.Fatalf("source=%+v", got.Source)
			}
			if got.Checked[len(got.Checked)-1].Status != tt.state {
				t.Fatalf("checked=%+v", got.Checked)
			}
			afterData, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterData) != string(data) || !after.ModTime().Equal(before.ModTime()) || transport.requests != 0 {
				t.Fatal("probe changed credentials or attempted HTTP")
			}
		})
	}
}

func Test_ProbeSignIn_reports_locked_or_unreadable_without_fallback(t *testing.T) {
	for _, tt := range []struct {
		err   error
		state string
	}{{ErrKeychainLocked, "locked"}, {ErrKeychainUnavailable, "unreadable"}, {ErrCredentialsMissing, "signed_out"}} {
		home := t.TempDir()
		client := New(Options{HomeDir: home, ConfigDir: filepath.Join(home, "work"), Keychain: &fakeKeychain{err: tt.err}})
		client.goos = "darwin"
		got := client.ProbeSignIn(context.Background())
		if got.State != tt.state || len(got.Checked) != 2 {
			t.Fatalf("probe=%+v", got)
		}
		if tt.state != "signed_out" && (got.Source == nil || got.Source.Kind != "keychain") {
			t.Fatalf("source=%+v", got.Source)
		}
	}
}
