package opencodego

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Provider_Fetch_reads_v1_console_sign_in(t *testing.T) {
	// Given a V1 database that holds only a console account
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q", *data.Error)
	}
	if data.Auth.State != "signed_in" || data.Auth.Source == nil || data.Auth.Source.Kind != "cli" || data.Auth.Source.Name != "OpenCode console" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if data.Auth.SignInCommand != nil || data.ReauthCommand == nil || *data.ReauthCommand != "opencode console login" {
		t.Fatalf("sign-in command = %v reauth command = %v", data.Auth.SignInCommand, data.ReauthCommand)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer console-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
	orgs := server.orgHeaders()
	if len(orgs) != 1 || orgs[0] != "wrk_fixture" {
		t.Fatalf("organization headers = %v", orgs)
	}
}

func Test_Provider_Fetch_key_wins_over_console_sign_in(t *testing.T) {
	// Given an auth.json key beside a console account
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"key-token"}}`)
	setModTime(t, authPath, millisAt(-time.Hour))
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI" {
		t.Fatalf("source = %#v", data.Auth.Source)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer key-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
	orgs := server.orgHeaders()
	if len(orgs) != 1 || orgs[0] != "" {
		t.Fatalf("organization headers = %v, want none", orgs)
	}
}

func Test_Provider_Fetch_reports_expired_console_sign_in(t *testing.T) {
	// Given a console account whose token has expired
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(-time.Minute)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if !data.NeedsReauth || data.Auth.State != "expired" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if errorText(data) != fmt.Sprintf(consoleExpiredFormat, consoleSignInCommand) {
		t.Fatalf("error = %q", errorText(data))
	}
	if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != "opencode console login" {
		t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
	}
	if data.ReauthCommand == nil || *data.ReauthCommand != "opencode console login" {
		t.Fatalf("reauth command = %v", data.ReauthCommand)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func Test_Provider_Fetch_reports_a_rejected_console_sign_in(t *testing.T) {
	// Given a console sign-in the usage endpoint rejects
	server := newUsageServer(t, http.StatusUnauthorized, "")
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if !data.NeedsReauth || data.Auth.State != "expired" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if errorText(data) != fmt.Sprintf(consoleRejectedFormat, consoleSignInCommand) {
		t.Fatalf("error = %q", errorText(data))
	}
	if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != "opencode console login" {
		t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
	}
	if _, _, requests := server.recorded(); requests != 1 {
		t.Fatalf("requests = %d, want one", requests)
	}
}

func Test_Provider_Fetch_console_sign_in_respects_the_import_marker(t *testing.T) {
	tests := []struct {
		name        string
		updated     time.Duration
		wantState   string
		wantRequest bool
	}{
		{name: "before the import marker", updated: -2 * time.Hour, wantState: "signed_out"},
		{name: "after the import marker", updated: 0, wantState: "signed_in", wantRequest: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given a V2 database whose console account the CLI refreshed
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
			home := t.TempDir()
			database := filepath.Join(localDataDirectory(home), "opencode.db")
			createConsoleTables(t, database)
			insertConsoleSignIn(t, database, consoleFixture{
				id:           "acct",
				email:        "fixture@example.com",
				url:          server.server.URL + "/console",
				accessToken:  "console-token",
				refreshToken: "refresh-token",
				expiryMS:     int64Ptr(millisAt(time.Hour)),
				createdMS:    millisAt(-3 * time.Hour),
				updatedMS:    millisAt(test.updated),
			}, strPtr("wrk_fixture"))
			claimV2Database(t, database, millisAt(-time.Hour))
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then
			if data.Auth.State != test.wantState {
				t.Fatalf("auth = %#v", data.Auth)
			}
			auths, _, requests := server.recorded()
			if !test.wantRequest {
				if requests != 0 {
					t.Fatalf("requests = %d, want none", requests)
				}
				return
			}
			if requests != 1 || len(auths) != 1 || auths[0] != "Bearer console-token" {
				t.Fatalf("requests = %d authorizations = %v", requests, auths)
			}
		})
	}
}

func Test_Provider_Fetch_credentials_path_reads_a_console_account(t *testing.T) {
	// Given an explicit database holding only a console account, plus an import
	// marker later than the account that discovery would treat as a sign-out
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	path := filepath.Join(t.TempDir(), "console.db")
	createConsoleTables(t, path)
	insertConsoleSignIn(t, path, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-2 * time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	claimV2Database(t, path, millisAt(0))
	provider := NewProvider(Options{CredentialsPath: path, Environment: testEnvironment(t, t.TempDir(), nil), UsageURL: server.url(), Now: func() time.Time { return fixedNow }})

	// When
	data := fetch(t, provider)

	// Then
	if data.Auth.State != "signed_in" || data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode console" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	want := []usage.AuthChecked{
		{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode console", Status: "signed_in"},
	}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
	if data.ReauthCommand == nil || *data.ReauthCommand != consoleSignInCommand {
		t.Fatalf("reauth command = %v", data.ReauthCommand)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer console-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
	orgs := server.orgHeaders()
	if len(orgs) != 1 || orgs[0] != "wrk_fixture" {
		t.Fatalf("organization headers = %v", orgs)
	}
}
