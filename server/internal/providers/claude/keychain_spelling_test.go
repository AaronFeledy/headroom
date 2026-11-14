package claude

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type spellingKeychain struct {
	items    map[string][]byte
	errors   map[string]error
	services []string
}

func (k *spellingKeychain) Read(_ context.Context, service string) ([]byte, error) {
	k.services = append(k.services, service)
	if err := k.errors[service]; err != nil {
		return nil, err
	}
	if data, ok := k.items[service]; ok {
		return data, nil
	}
	return nil, ErrCredentialsMissing
}

func Test_Client_account_keychain_toggles_only_missing_trailing_slash_spelling(t *testing.T) {
	for _, tt := range []struct {
		name, suffix     string
		exact, alternate bool
		err              error
		wantProbes       int
	}{
		{"exact found", "", true, true, nil, 1},
		{"add slash", "", false, true, nil, 2},
		{"strip slashes", "///", false, true, nil, 2},
		{"locked", "", false, true, ErrKeychainLocked, 1},
		{"unavailable", "", false, true, ErrKeychainUnavailable, 1},
		{"malformed", "", true, true, ErrCredentialsMalformed, 1},
		{"both missing", "", false, false, ErrCredentialsMissing, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given: preserve an interior spelling that filepath.Clean would change.
			home := t.TempDir()
			dir := home + "/other/../.claude-work" + tt.suffix
			toggled := dir + "/"
			if tt.suffix != "" {
				toggled = home + "/other/../.claude-work"
			}
			exactService, alternateService := keychainService(dir), keychainService(toggled)
			valid := []byte(claudeCredentialsJSON(credentialFixture{Access: "exact", Refresh: "r", ExpiresAt: 4102444800000}))
			keychain := &spellingKeychain{items: map[string][]byte{}, errors: map[string]error{}}
			if tt.exact {
				keychain.items[exactService] = valid
			}
			if tt.alternate {
				keychain.items[alternateService] = []byte(claudeCredentialsJSON(credentialFixture{Access: "alternate", Refresh: "r", ExpiresAt: 4102444800000}))
			}
			if errors.Is(tt.err, ErrCredentialsMalformed) {
				keychain.items[exactService] = []byte("{")
			} else if tt.err != nil && !errors.Is(tt.err, ErrCredentialsMissing) {
				keychain.errors[exactService] = tt.err
			}
			client := New(Options{HomeDir: home, ConfigDir: dir, Keychain: keychain, Account: &usage.Account{ID: "work", Label: "Work"}})
			client.goos = "darwin"
			// When
			creds, err := client.loadCredentials(context.Background())
			// Then
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			wantServices := []string{exactService}
			if tt.wantProbes == 2 {
				wantServices = append(wantServices, alternateService)
			}
			if !reflect.DeepEqual(keychain.services, wantServices) {
				t.Fatalf("probes = %v, want %v", keychain.services, wantServices)
			}
			status := "signed_in"
			switch tt.err {
			case ErrCredentialsMissing:
				status = "signed_out"
			case ErrKeychainLocked:
				status = "locked"
			case ErrKeychainUnavailable, ErrCredentialsMalformed:
				status = "unreadable"
			}
			want := []usage.AuthChecked{{Kind: "keychain", Name: "Claude Code", Status: status}}
			if tt.err != nil && !errors.Is(tt.err, ErrCredentialsMalformed) {
				want = append(want, usage.AuthChecked{Kind: "cli", Name: "Claude Code", Status: "signed_out"})
			}
			if !reflect.DeepEqual(client.checked, want) {
				t.Fatalf("checked = %+v, want %+v", client.checked, want)
			}
			if tt.err == nil {
				if creds == nil {
					t.Fatal("successful discovery returned no credentials")
				}
				wantToken := "alternate"
				if tt.exact {
					wantToken = "exact"
				}
				if creds.accessToken != wantToken {
					t.Fatal("wrong spelling selected")
				}
			}
		})
	}
}

func Test_Client_account_keychain_expands_home_without_cleaning(t *testing.T) {
	// Given
	home := t.TempDir()
	keychain := &fakeKeychain{data: []byte(claudeCredentialsJSON(credentialFixture{Access: "fixture", Refresh: "r", ExpiresAt: 4102444800000}))}
	client := New(Options{HomeDir: home, ConfigDir: "~/other/../work/", Keychain: keychain})
	client.goos = "darwin"
	// When
	_, err := client.loadCredentials(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keychain.services, []string{keychainService(home + "/other/../work/")}) {
		t.Fatal("Keychain spelling was cleaned")
	}
	if filepath.Clean(client.paths.configDir) == client.paths.configDir {
		t.Fatal("fixture did not distinguish cleaned spelling")
	}
}

func Test_Client_root_account_slash_toggle_never_reads_default_keychain_item(t *testing.T) {
	// Given
	keychain := &spellingKeychain{items: map[string][]byte{
		"Claude Code-credentials":          []byte(claudeCredentialsJSON(credentialFixture{Access: "default", Refresh: "r", ExpiresAt: 4102444800000})),
		"Claude Code-credentials-e3b0c442": []byte(claudeCredentialsJSON(credentialFixture{Access: "account", Refresh: "r", ExpiresAt: 4102444800000})),
	}}
	client := New(Options{HomeDir: t.TempDir(), ConfigDir: "/", Keychain: keychain, Account: &usage.Account{ID: "root", Label: "Root"}})
	client.goos = "darwin"
	// When
	creds, err := client.loadCredentials(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if creds.accessToken != "account" {
		t.Fatal("slash toggle selected the default account")
	}
}
