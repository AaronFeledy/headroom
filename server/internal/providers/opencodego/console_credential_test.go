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

func Test_Provider_Fetch_v2_console_oauth_row_sends_its_organization(t *testing.T) {
	// Given a V2 OAuth row holding a console organization
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-3*time.Hour))
	value := fmt.Sprintf(`{"type":"oauth","access":"oauth-token","expires":%d,"metadata":{"server":%q,"orgID":"wrk_fixture"}}`,
		fixedNow.Add(time.Hour).UnixMilli(), server.server.URL+"/console")
	insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: value, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Auth.State != "signed_in" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if data.Auth.Source == nil || data.Auth.Source.Kind != "cli" || data.Auth.Source.Name != "OpenCode console" {
		t.Fatalf("source = %#v", data.Auth.Source)
	}
	if data.ReauthCommand == nil || *data.ReauthCommand != signInCommandText {
		t.Fatalf("reauth command = %v", data.ReauthCommand)
	}
	want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode console", Status: "signed_in"}}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer oauth-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
	orgs := server.orgHeaders()
	if len(orgs) != 1 || orgs[0] != "wrk_fixture" {
		t.Fatalf("organization headers = %v", orgs)
	}
}

func Test_Provider_Fetch_ignores_a_v2_oauth_row_for_another_server(t *testing.T) {
	// Given a V2 OAuth row whose console server is another origin
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-3*time.Hour))
	value := fmt.Sprintf(`{"type":"oauth","access":"oauth-token","expires":%d,"metadata":{"server":"https://other.example/console","orgID":"wrk_fixture"}}`,
		fixedNow.Add(time.Hour).UnixMilli())
	insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: value, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then the row is not a candidate, so the key store produced nothing
	if data.Auth.State != "signed_out" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	want := []usage.AuthChecked{
		{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
	}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func Test_Provider_Fetch_reports_v2_console_credential_failure(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		expires     time.Duration
		wantMessage string
		wantChecked string
		wantRequest bool
	}{
		{
			name:        "expired",
			expires:     -time.Minute,
			wantMessage: fmt.Sprintf(consoleExpiredFormat, signInCommandText),
			wantChecked: "expired",
		},
		{
			name:        "unauthorized",
			status:      http.StatusUnauthorized,
			expires:     time.Hour,
			wantMessage: fmt.Sprintf(consoleRejectedFormat, signInCommandText),
			wantChecked: "signed_in",
			wantRequest: true,
		},
		{
			name:        "forbidden",
			status:      http.StatusForbidden,
			expires:     time.Hour,
			wantMessage: fmt.Sprintf(consoleRejectedFormat, signInCommandText),
			wantChecked: "signed_in",
			wantRequest: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given a V2 OAuth row that is expired or rejected upstream
			server := newUsageServer(t, test.status, `{"usage":{"rolling":{"percent":42}}}`)
			home := t.TempDir()
			database := filepath.Join(localDataDirectory(home), "opencode.db")
			createCredentialDatabase(t, database)
			claimV2Database(t, database, millisAt(-3*time.Hour))
			value := fmt.Sprintf(`{"type":"oauth","access":"oauth-token","expires":%d,"metadata":{"orgID":"wrk_fixture"}}`,
				fixedNow.Add(test.expires).UnixMilli())
			insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: value, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
			provider := newProvider(t, testEnvironment(t, home, nil), server.url())

			// When
			data := fetch(t, provider)

			// Then both console kinds explain themselves and name their command
			if !data.NeedsReauth || data.Auth.State != "expired" {
				t.Fatalf("auth = %#v", data.Auth)
			}
			if errorText(data) != test.wantMessage {
				t.Fatalf("error = %q, want %q", errorText(data), test.wantMessage)
			}
			if data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode console" {
				t.Fatalf("source = %#v", data.Auth.Source)
			}
			if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != signInCommandText {
				t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
			}
			if data.ReauthCommand == nil || *data.ReauthCommand != signInCommandText {
				t.Fatalf("reauth command = %v", data.ReauthCommand)
			}
			want := []usage.AuthChecked{{Kind: "cli", Name: "OpenCode console", Status: test.wantChecked}}
			if !slices.Equal(data.Auth.Checked, want) {
				t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
			}
			_, _, requests := server.recorded()
			if (requests == 1) != test.wantRequest {
				t.Fatalf("requests = %d, wantRequest = %v", requests, test.wantRequest)
			}
		})
	}
}
