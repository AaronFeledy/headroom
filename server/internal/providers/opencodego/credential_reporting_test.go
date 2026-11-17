package opencodego

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func Test_Provider_Fetch_configured_rows_report_the_selected_kind(t *testing.T) {
	tests := []struct {
		name, value, state, source, status, message string
	}{
		{"key", `{"type":"key","key":"token"}`, "signed_in", "OpenCode Go CLI", "signed_in", ""},
		{"invalid console", `{"type":"oauth","access":"bad token","expires":0}`, "expired", "OpenCode console", "unreadable", "OpenCode Go credentials are invalid. Run `opencode auth login` again."},
		{"expired console", fmt.Sprintf(`{"type":"oauth","access":"token","expires":%d}`, millisAt(-time.Hour)), "expired", "OpenCode console", "expired", "OpenCode console sign-in expired. Open OpenCode to refresh it, or run `opencode auth login`."},
		{"other origin", `{"type":"oauth","access":"token","expires":0,"metadata":{"server":"https://other.example","orgID":"workspace"}}`, "signed_out", "", "signed_out", "OpenCode Go is not signed in. Run `opencode auth login` and choose OpenCode Go."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given an explicitly configured credential row without a migration marker
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
			path := filepath.Join(t.TempDir(), "opencode.db")
			createCredentialDatabase(t, path)
			insertCredential(t, path, credentialFixture{id: "go", integration: "opencode-go", value: test.value, active: activeFlag(1)})
			provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

			// When
			data := fetch(t, provider)

			// Then a non-candidate row is still reported as an examined key store
			name := test.source
			if name == "" {
				name = "OpenCode Go CLI"
			}
			want := []usage.AuthChecked{{Kind: "cli", Name: name, Status: test.status}}
			if !slices.Equal(data.Auth.Checked, want) || data.Auth.State != test.state || errorText(data) != test.message {
				t.Fatalf("auth = %#v error = %q, want checked %#v state %q error %q", data.Auth, errorText(data), want, test.state, test.message)
			}
			if test.source == "" {
				if data.Auth.Source != nil {
					t.Fatalf("source = %#v, want nil", data.Auth.Source)
				}
			} else if data.Auth.Source == nil || data.Auth.Source.Kind != "cli" || data.Auth.Source.Name != test.source {
				t.Fatalf("source = %#v, want %q", data.Auth.Source, test.source)
			}
			if data.ReauthCommand == nil || *data.ReauthCommand != "opencode auth login" {
				t.Fatalf("reauth command = %v", data.ReauthCommand)
			}
			if test.state == "signed_in" {
				if data.Auth.SignInCommand != nil {
					t.Fatalf("sign-in command = %v, want nil", data.Auth.SignInCommand)
				}
			} else if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != "opencode auth login" {
				t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
			}
		})
	}
}

func Test_Provider_Fetch_checked_uses_the_shared_status_vocabulary(t *testing.T) {
	tests := []struct {
		name      string
		rows      []credentialFixture
		wantState string
		want      []usage.AuthChecked
	}{
		{
			name:      "signed in",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "signed_in",
			want:      []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_in"}},
		},
		{
			name:      "expired",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: fmt.Sprintf(`{"type":"oauth","access":"token","expires":%d}`, fixedNow.Add(-time.Hour).UnixMilli()), active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "expired",
			want:      []usage.AuthChecked{{Kind: "cli", Name: "OpenCode console", Status: "expired"}},
		},
		{
			name:      "unreadable",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `not json`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "expired",
			want:      []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI", Status: "unreadable"}},
		},
		{
			name:      "signed out",
			wantState: "signed_out",
			want: []usage.AuthChecked{
				{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
				{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given a V2 database holding one candidate or none
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
			home := t.TempDir()
			database := filepath.Join(localDataDirectory(home), "opencode.db")
			createCredentialDatabase(t, database)
			claimV2Database(t, database, millisAt(-3*time.Hour))
			for _, row := range test.rows {
				insertCredential(t, database, row)
			}
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Auth.State != test.wantState {
				t.Fatalf("auth = %#v, want state %q", data.Auth, test.wantState)
			}
			if !slices.Equal(data.Auth.Checked, test.want) {
				t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, test.want)
			}
		})
	}
}

func Test_Provider_Fetch_checked_lists_groups_in_discovery_order(t *testing.T) {
	// Given a local store that is empty and a WSL store holding a key
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	createCredentialDatabase(t, filepath.Join(localDataDirectory(home), "opencode.db"))
	wslHome := t.TempDir()
	wslDatabase := filepath.Join(wslHome, ".local", "share", "opencode", "opencode.db")
	createCredentialDatabase(t, wslDatabase)
	claimV2Database(t, wslDatabase, millisAt(-time.Hour))
	insertCredential(t, wslDatabase, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"wsl-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
	environment := winprofile.Environment{
		GOOS:    "windows",
		Home:    home,
		Root:    t.TempDir(),
		WSLHome: wslHome,
		Env:     func(string) string { return "" },
	}
	provider := newProvider(t, environment, server.url())

	// When
	data := fetch(t, provider)

	// Then the checked list follows discovery through the selected group
	want := []usage.AuthChecked{
		{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode Go CLI (WSL)", Status: "signed_in"},
	}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
	if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI (WSL)" {
		t.Fatalf("source = %#v", data.Auth.Source)
	}
}

func Test_Provider_Fetch_credentials_path_reports_checked_entries(t *testing.T) {
	t.Run("json key", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte(`{"opencode-go":{"type":"api","key":"json-token"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		data := fetch(t, provider)

		// Then
		want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_in"}}
		if !slices.Equal(data.Auth.Checked, want) {
			t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
		}
		if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI" || data.ReauthCommand == nil || *data.ReauthCommand != signInCommandText {
			t.Fatalf("auth = %#v", data.Auth)
		}
	})
	t.Run("database without rows", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := filepath.Join(t.TempDir(), "opencode.db")
		createCredentialDatabase(t, path)
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		data := fetch(t, provider)

		// Then the configured store and its console fallback are both listed
		want := []usage.AuthChecked{
			{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
			{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
		}
		if !slices.Equal(data.Auth.Checked, want) {
			t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
		}
		if errorText(data) != notSignedInMessage {
			t.Fatalf("error = %q", errorText(data))
		}
	})
	t.Run("missing file", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := filepath.Join(t.TempDir(), "missing.json")
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		data := fetch(t, provider)

		// Then
		want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"}}
		if !slices.Equal(data.Auth.Checked, want) {
			t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
		}
	})
	t.Run("WSL path carries the suffix", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := `\\wsl.localhost\Ubuntu\home\fixture\.local\share\opencode\opencode.db`
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		data := fetch(t, provider)

		// Then
		want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI (WSL)", Status: "signed_out"}}
		if !slices.Equal(data.Auth.Checked, want) {
			t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
		}
	})
}

func Test_Provider_Fetch_reports_an_unreadable_store_without_guidance(t *testing.T) {
	// Given an explicit SQLite header whose file is not a database
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	path := filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(path, append([]byte(sqliteHeader), []byte("not a database")...), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

	// When
	data := fetch(t, provider)

	// Then a read failure keeps no source or command, only the checked entry
	if data.Auth.State != "signed_out" || data.Auth.Source != nil {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if data.Auth.SignInCommand != nil || data.ReauthCommand != nil {
		t.Fatalf("sign-in command = %v reauth command = %v", data.Auth.SignInCommand, data.ReauthCommand)
	}
	if errorText(data) != "OpenCode Go credentials could not be read. Will retry." {
		t.Fatalf("error = %q", errorText(data))
	}
	want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode Go CLI", Status: "unreadable"}}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}
