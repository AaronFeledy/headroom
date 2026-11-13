package opencodego

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func Test_Provider_Fetch_orders_rows_like_the_cli(t *testing.T) {
	tests := []struct {
		name       string
		rows       []credentialFixture
		wantState  string
		wantAuth   string
		wantError  string
		wantReauth bool
	}{
		{
			name:      "a legacy row without an active flag",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"legacy-row"}`, createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer legacy-row",
		},
		{
			name: "the newest legacy row wins",
			rows: []credentialFixture{
				{id: "older", integration: "opencode-go", value: `{"type":"key","key":"older-row"}`, createdMS: millisAt(-2 * time.Hour), updatedMS: millisAt(-time.Hour)},
				{id: "newer", integration: "opencode-go", value: `{"type":"key","key":"newer-row"}`, createdMS: millisAt(-time.Hour), updatedMS: millisAt(-2 * time.Hour)},
			},
			wantState: "signed_in", wantAuth: "Bearer newer-row",
		},
		{
			name: "an active row beats a newer legacy row",
			rows: []credentialFixture{
				{id: "active", integration: "opencode-go", value: `{"type":"key","key":"active-row"}`, active: activeFlag(1), createdMS: millisAt(-2 * time.Hour), updatedMS: millisAt(0)},
				{id: "legacy", integration: "opencode-go", value: `{"type":"key","key":"legacy-row"}`, createdMS: millisAt(-time.Hour), updatedMS: millisAt(time.Hour)},
			},
			wantState: "signed_in", wantAuth: "Bearer active-row",
		},
		{
			name:      "the generic integration is used without an OpenCode Go row",
			rows:      []credentialFixture{{id: "generic", integration: "opencode", value: `{"type":"key","key":"generic-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer generic-token",
		},
		{
			name: "the generic integration never outranks an OpenCode Go row",
			rows: []credentialFixture{
				{id: "generic", integration: "opencode", value: `{"type":"key","key":"generic-token"}`, active: activeFlag(1), createdMS: millisAt(time.Hour), updatedMS: millisAt(time.Hour)},
				{id: "go", integration: "opencode-go", value: `{"type":"key","key":"go-row"}`, createdMS: millisAt(-time.Hour), updatedMS: millisAt(-time.Hour)},
			},
			wantState: "signed_in", wantAuth: "Bearer go-row",
		},
		{
			name:      "a deactivated row is still selected",
			rows:      []credentialFixture{{id: "go", integration: "opencode-go", value: `{"type":"key","key":"deactivated-row"}`, active: activeFlag(0), createdMS: millisAt(0), updatedMS: millisAt(0)}},
			wantState: "signed_in", wantAuth: "Bearer deactivated-row",
		},
		{
			name: "two active rows are ambiguous",
			rows: []credentialFixture{
				{id: "one", integration: "opencode-go", value: `{"type":"key","key":"first-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)},
				{id: "two", integration: "opencode-go", value: `{"type":"key","key":"second-token"}`, active: activeFlag(1), createdMS: millisAt(time.Hour), updatedMS: millisAt(time.Hour)},
			},
			wantState: "expired", wantReauth: true,
			wantError: "OpenCode Go credentials are ambiguous. Run `opencode auth login` again.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
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
			if data.Auth.State != test.wantState || data.NeedsReauth != test.wantReauth {
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

func Test_Provider_Fetch_decodes_stored_credential_values(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantState  string
		wantAuth   string
		wantError  string
		wantReauth bool
	}{
		{
			name:      "oauth access until expiry",
			value:     fmt.Sprintf(`{"type":"oauth","access":"oauth-token","expires":%d}`, fixedNow.Add(time.Hour).UnixMilli()),
			wantState: "signed_in", wantAuth: "Bearer oauth-token",
		},
		{
			name:      "zero expiry never expires",
			value:     `{"type":"oauth","access":"never-expires","expires":0}`,
			wantState: "signed_in", wantAuth: "Bearer never-expires",
		},
		{
			name:      "expired oauth is refused",
			value:     fmt.Sprintf(`{"type":"oauth","access":"oauth-token","expires":%d}`, fixedNow.Add(-time.Hour).UnixMilli()),
			wantState: "expired", wantReauth: true,
			wantError: "OpenCode Go auth expired. Run `opencode auth login` again.",
		},
		{
			name:      "an unusable stored type requires sign-in",
			value:     `{"type":"external","key":"nope"}`,
			wantState: "expired", wantReauth: true,
			wantError: "OpenCode Go credentials are invalid. Run `opencode auth login` again.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
			home := t.TempDir()
			database := filepath.Join(localDataDirectory(home), "opencode.db")
			createCredentialDatabase(t, database)
			claimV2Database(t, database, millisAt(-3*time.Hour))
			insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: test.value, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Auth.State != test.wantState || data.NeedsReauth != test.wantReauth {
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
