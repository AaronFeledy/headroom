package opencodego

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func Test_Provider_Fetch_requires_an_active_console_organization(t *testing.T) {
	// Given a console account without an active organization
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
	}, nil)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Auth.State != "signed_out" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if errorText(data) != noWorkspaceMessage {
		t.Fatalf("error = %q", errorText(data))
	}
	if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != consoleSwitchCommand {
		t.Fatalf("sign-in command = %v", data.Auth.SignInCommand)
	}
	if data.ReauthCommand == nil || *data.ReauthCommand != consoleSwitchCommand {
		t.Fatalf("reauth command = %v", data.ReauthCommand)
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

func Test_Provider_Fetch_never_sends_a_console_token_to_another_origin(t *testing.T) {
	// Given a console account whose URL names another server, with credentials
	// and a path that must never reach the message
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          "https://fixture-user:secret@other.example:8443/console?session=hidden",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, strPtr("wrk_fixture"))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then only the hostname identifies the other server
	if data.Auth.State != "signed_out" || data.Auth.Source != nil {
		t.Fatalf("auth = %#v", data.Auth)
	}
	want := fmt.Sprintf(otherOriginFormat, "other.example")
	if errorText(data) != want {
		t.Fatalf("error = %q, want %q", errorText(data), want)
	}
	for _, secret := range []string{"fixture-user", "secret", "/console", "session=hidden", "8443"} {
		if strings.Contains(errorText(data), secret) {
			t.Fatalf("error %q contains %q", errorText(data), secret)
		}
	}
	if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != signInCommandText || data.ReauthCommand == nil || *data.ReauthCommand != signInCommandText {
		t.Fatalf("sign-in command = %v reauth command = %v", data.Auth.SignInCommand, data.ReauthCommand)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func Test_Provider_Fetch_names_an_unparsed_console_origin_as_another_server(t *testing.T) {
	// Given a console account whose stored URL has no host to show
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, database)
	insertConsoleSignIn(t, database, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          "not a url",
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
	if want := fmt.Sprintf(otherOriginFormat, unparsedConsoleHost); errorText(data) != want {
		t.Fatalf("error = %q, want %q", errorText(data), want)
	}
	if data.Auth.State != "signed_out" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func Test_Provider_Fetch_keeps_first_console_guidance_across_kernels(t *testing.T) {
	for _, localWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprintf("local workspace %t", localWorkspace), func(t *testing.T) {
			// Given different signed-out console issues in local and WSL stores
			server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
			home, wslHome := t.TempDir(), t.TempDir()
			for index, directory := range []string{home, wslHome} {
				path := filepath.Join(localDataDirectory(directory), "opencode.db")
				createConsoleTables(t, path)
				account := consoleFixture{id: "acct", email: "fixture@example.com", url: server.server.URL, accessToken: "token"}
				var orgID *string
				if (index == 0) == localWorkspace {
					account.url = "https://other.example/console"
					orgID = strPtr("workspace")
				}
				insertConsoleSignIn(t, path, account, orgID)
			}
			environment := winprofile.Environment{GOOS: "windows", Home: home, Root: t.TempDir(), WSLHome: wslHome, Env: func(string) string { return "" }}
			provider := newProvider(t, environment, server.url())

			// When
			data := fetch(t, provider)

			// Then the first issue supplies guidance while every examined store stays listed
			message := "Your OpenCode console sign-in has no active workspace. Run `opencode console switch` to choose one."
			command := "opencode console switch"
			if localWorkspace {
				message = "Your OpenCode console sign-in is for other.example, so Headroom can't read OpenCode Go usage with it. Run `opencode auth login` to add an OpenCode Go API key."
				command = "opencode auth login"
			}
			want := []usage.AuthChecked{
				{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
				{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
				{Kind: "cli", Name: "OpenCode Go CLI (WSL)", Status: "signed_out"},
				{Kind: "cli", Name: "OpenCode console (WSL)", Status: "signed_out"},
			}
			if !slices.Equal(data.Auth.Checked, want) || data.Auth.State != "signed_out" || data.Auth.Source != nil || errorText(data) != message {
				t.Fatalf("auth = %#v error = %q, want checked %#v error %q", data.Auth, errorText(data), want, message)
			}
			if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != command || data.ReauthCommand == nil || *data.ReauthCommand != command {
				t.Fatalf("sign-in command = %v reauth command = %v, want %q", data.Auth.SignInCommand, data.ReauthCommand, command)
			}
			if _, _, requests := server.recorded(); requests != 0 {
				t.Fatalf("requests = %d, want none", requests)
			}
		})
	}
}

func Test_Provider_Fetch_console_guidance_does_not_mask_a_cross_kernel_key(t *testing.T) {
	// Given a local console sign-in with no workspace and a WSL API key
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	localDatabase := filepath.Join(localDataDirectory(home), "opencode.db")
	createConsoleTables(t, localDatabase)
	insertConsoleSignIn(t, localDatabase, consoleFixture{
		id:           "acct",
		email:        "fixture@example.com",
		url:          server.server.URL + "/console",
		accessToken:  "console-token",
		refreshToken: "refresh-token",
		expiryMS:     int64Ptr(millisAt(time.Hour)),
		createdMS:    millisAt(-time.Hour),
		updatedMS:    millisAt(-time.Hour),
	}, nil)
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

	// Then the later key wins and the earlier guidance is not reported
	if data.Auth.State != "signed_in" || errorText(data) != "" {
		t.Fatalf("auth = %#v error = %q", data.Auth, errorText(data))
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer wsl-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
	want := []usage.AuthChecked{
		{Kind: "cli", Name: "OpenCode Go CLI", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode console", Status: "signed_out"},
		{Kind: "cli", Name: "OpenCode Go CLI (WSL)", Status: "signed_in"},
	}
	if !slices.Equal(data.Auth.Checked, want) {
		t.Fatalf("checked = %#v, want %#v", data.Auth.Checked, want)
	}
}
