package opencodego

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func Test_Provider_Fetch_credentials_path_override_accepts_json_and_sqlite(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := filepath.Join(t.TempDir(), "credentials.dat")
		if err := os.WriteFile(path, []byte(`{"opencode-go":{"type":"api","key":"override-json"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer override-json" {
			t.Fatalf("authorizations = %v", auths)
		}
	})
	t.Run("sqlite", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		path := filepath.Join(t.TempDir(), "credentials.bin")
		createCredentialDatabase(t, path)
		insertCredential(t, path, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"override-sqlite"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
		provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

		// When
		fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer override-sqlite" {
			t.Fatalf("authorizations = %v", auths)
		}
	})
}

func Test_Provider_Fetch_honors_XDG_DATA_HOME_and_OPENCODE_DB(t *testing.T) {
	t.Run("absolute XDG data home", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		dataHome := filepath.Join(t.TempDir(), "xdg")
		directory := filepath.Join(dataHome, "opencode")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"opencode-go":{"type":"api","key":"xdg-token"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		environment := testEnvironment(t, t.TempDir(), map[string]string{"XDG_DATA_HOME": dataHome})
		provider := newProvider(t, environment, server.url())

		// When
		fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer xdg-token" {
			t.Fatalf("authorizations = %v", auths)
		}
	})
	t.Run("relative OPENCODE_DB", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		home := t.TempDir()
		database := filepath.Join(localDataDirectory(home), "custom.db")
		createCredentialDatabase(t, database)
		claimV2Database(t, database, millisAt(-time.Hour))
		insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"relative-db"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
		environment := testEnvironment(t, home, map[string]string{"OPENCODE_DB": "custom.db"})
		provider := newProvider(t, environment, server.url())

		// When
		fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer relative-db" {
			t.Fatalf("authorizations = %v", auths)
		}
	})
	t.Run("memory database skips the database", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		home := t.TempDir()
		database := filepath.Join(localDataDirectory(home), "opencode.db")
		createCredentialDatabase(t, database)
		claimV2Database(t, database, millisAt(-time.Hour))
		insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"db-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
		authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"legacy-token"}}`)
		setModTime(t, authPath, millisAt(-30*time.Minute))
		environment := testEnvironment(t, home, map[string]string{"OPENCODE_DB": ":memory:"})
		provider := newProvider(t, environment, server.url())

		// When
		fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer legacy-token" {
			t.Fatalf("authorizations = %v", auths)
		}
	})
}

func Test_Provider_Fetch_picks_up_sign_in_and_sign_out_without_restart(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	home := t.TempDir()
	path := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"first-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	first := fetch(t, provider)
	if err := os.WriteFile(path, []byte(`{"opencode-go":{"type":"api","key":"second-token-longer"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := fetch(t, provider)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	third := fetch(t, provider)

	// Then
	auths, _, requests := server.recorded()
	if first.Auth.State != "signed_in" || second.Auth.State != "signed_in" || third.Auth.State != "signed_out" {
		t.Fatalf("states = %q/%q/%q", first.Auth.State, second.Auth.State, third.Auth.State)
	}
	if requests != 2 || len(auths) != 2 || auths[0] != "Bearer first-token" || auths[1] != "Bearer second-token-longer" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}

func Test_Provider_Fetch_rereads_v2_changes(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-time.Hour))
	insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"first-db-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	first := fetch(t, provider)
	updateCredential(t, database, `UPDATE credential SET value = ? WHERE id = ?`, `{"type":"key","key":"second-db-token"}`, "go")
	second := fetch(t, provider)
	// Signing out deletes the row; a row that merely loses its active flag is still selected.
	updateCredential(t, database, `DELETE FROM credential WHERE id = ?`, "go")
	third := fetch(t, provider)

	// Then
	auths, _, requests := server.recorded()
	if first.Auth.State != "signed_in" || second.Auth.State != "signed_in" || third.Auth.State != "signed_out" {
		t.Fatalf("states = %q/%q/%q", first.Auth.State, second.Auth.State, third.Auth.State)
	}
	if requests != 2 || len(auths) != 2 || auths[0] != "Bearer first-db-token" || auths[1] != "Bearer second-db-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}

func Test_Provider_Fetch_cross_kernel_discovery(t *testing.T) {
	t.Run("windows checks WSL homes", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		wslHome := t.TempDir()
		database := filepath.Join(wslHome, ".local", "share", "opencode", "opencode.db")
		createCredentialDatabase(t, database)
		claimV2Database(t, database, millisAt(-time.Hour))
		insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"wsl-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
		environment := winprofile.Environment{
			GOOS:    "windows",
			Home:    t.TempDir(),
			Root:    t.TempDir(),
			WSLHome: wslHome,
			Env:     func(string) string { return "" },
		}
		provider := newProvider(t, environment, server.url())

		// When
		data := fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer wsl-token" {
			t.Fatalf("authorizations = %v", auths)
		}
		if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI (WSL)" {
			t.Fatalf("source = %#v", data.Auth.Source)
		}
	})
	t.Run("WSL checks Windows profiles", func(t *testing.T) {
		// Given
		server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
		root := t.TempDir()
		directory := filepath.Join(root, "mnt", "c", "Users", "alice", ".local", "share", "opencode")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"opencode-go":{"type":"api","key":"windows-token"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		environment := winprofile.Environment{
			GOOS: "linux",
			Home: t.TempDir(),
			Root: root,
			Env: func(key string) string {
				if key == "WSL_DISTRO_NAME" {
					return "fixture"
				}
				return ""
			},
		}
		provider := newProvider(t, environment, server.url())

		// When
		data := fetch(t, provider)

		// Then
		auths, _, _ := server.recorded()
		if len(auths) != 1 || auths[0] != "Bearer windows-token" {
			t.Fatalf("authorizations = %v", auths)
		}
		if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI (Windows)" {
			t.Fatalf("source = %#v", data.Auth.Source)
		}
	})
}
