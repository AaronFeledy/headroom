package opencodego

import (
	"path/filepath"
	"testing"
	"time"
)

func Test_Provider_Fetch_selects_live_credential_candidates(t *testing.T) {
	tests := []struct {
		name              string
		auth              string
		authMillis        int64
		claimed           bool
		migrationTable    bool
		unrelatedDatabase bool
		rows              []credentialFixture
		wantState         string
		wantAuth          string
		wantError         string
	}{
		{
			name: "a database without a credential table",
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-2 * time.Hour),
			unrelatedDatabase: true, wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name: "unclaimed rows cannot override V1 auth",
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-2 * time.Hour),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"ignored-token"}`, updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name: "migration table without import marker is still V1", migrationTable: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-2 * time.Hour),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"ignored-token"}`, updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name:      "unclaimed rows without auth are signed out",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"ignored-token"}`, updatedMS: millisAt(0)}},
			wantState: "signed_out", wantError: "OpenCode Go is not signed in. Run `opencode auth login`.",
		},
		{
			name: "V2 logout cannot revive imported auth", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"imported-token"}}`, authMillis: millisAt(-4 * time.Hour),
			wantState: "signed_out", wantError: "OpenCode Go is not signed in. Run `opencode auth login`.",
		},
		{
			name: "auth at the import boundary remains imported", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"imported-token"}}`, authMillis: millisAt(-3 * time.Hour),
			wantState: "signed_out", wantError: "OpenCode Go is not signed in. Run `opencode auth login`.",
		},
		{
			name: "V1 sign-in after import survives an empty table", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-2 * time.Hour),
			wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name: "selected row newer than live auth wins", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-2 * time.Hour),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(-time.Hour)}},
			wantState: "signed_in", wantAuth: "Bearer table-token",
		},
		{
			name: "live auth newer than selected row wins", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(0),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(-time.Hour)}},
			wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name: "equal timestamps prefer the table", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(0),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer table-token",
		},
		{
			name: "unrelated rows cannot revive imported auth", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"imported-token"}}`, authMillis: millisAt(-4 * time.Hour),
			rows:      []credentialFixture{{id: "cursor", integration: "cursor", value: `{"type":"key","key":"cursor-token"}`, updatedMS: millisAt(0)}},
			wantState: "signed_out", wantError: "OpenCode Go is not signed in. Run `opencode auth login`.",
		},
		{
			name: "V1 rewrite for another provider leaves V2 signed in", claimed: true,
			auth: `{"cursor":{"type":"api","key":"cursor-token"}}`, authMillis: millisAt(0),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(-time.Hour)}},
			wantState: "signed_in", wantAuth: "Bearer table-token",
		},
		{
			name: "unrelated newer row does not change candidate recency", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-time.Hour),
			rows: []credentialFixture{
				{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(-2 * time.Hour)},
				{id: "cursor", integration: "cursor", value: `{"type":"key","key":"cursor-token"}`, updatedMS: millisAt(0)},
			},
			wantState: "signed_in", wantAuth: "Bearer auth-token",
		},
		{
			name: "invalid winning row never falls back", claimed: true,
			auth: `{"opencode-go":{"type":"api","key":"auth-token"}}`, authMillis: millisAt(-time.Hour),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":""}`, updatedMS: millisAt(0)}},
			wantState: "expired", wantError: "OpenCode Go credentials are invalid. Run `opencode auth login` again.",
		},
		{
			name: "invalid winning auth never falls back", claimed: true,
			auth: `{`, authMillis: millisAt(0),
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"table-token"}`, updatedMS: millisAt(-time.Hour)}},
			wantState: "expired", wantError: "OpenCode Go credentials are invalid. Run `opencode auth login` again.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
			home := t.TempDir()
			if test.auth != "" {
				authPath := writeLegacyAuth(t, home, test.auth)
				setModTime(t, authPath, test.authMillis)
			}
			database := filepath.Join(localDataDirectory(home), "opencode.db")
			if test.unrelatedDatabase {
				createUnrelatedDatabase(t, database)
			} else {
				createCredentialDatabase(t, database)
			}
			if test.claimed {
				claimV2Database(t, database, millisAt(-3*time.Hour))
			}
			if test.migrationTable {
				updateCredential(t, database, `CREATE TABLE "migration" (id TEXT PRIMARY KEY, time_completed INTEGER NOT NULL)`)
				updateCredential(t, database, `INSERT INTO migration (id, time_completed) VALUES ('20260611035744_credential', ?)`, millisAt(-3*time.Hour))
			}
			for _, row := range test.rows {
				insertCredential(t, database, row)
			}
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Auth.State != test.wantState {
				t.Fatalf("auth = %#v", data.Auth)
			}
			if errorText(data) != test.wantError {
				t.Fatalf("error = %q, want %q", errorText(data), test.wantError)
			}
			auths, _, requests := server.recorded()
			if test.wantAuth == "" {
				if requests != 0 {
					t.Fatalf("requests = %d, want none", requests)
				}
				return
			}
			if requests != 1 || len(auths) != 1 || auths[0] != test.wantAuth {
				t.Fatalf("requests = %d authorizations = %v, want %s", requests, auths, test.wantAuth)
			}
		})
	}
}
