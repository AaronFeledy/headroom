package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func newTestClient(t *testing.T, options Options) *Client {
	t.Helper()
	if options.Discovery == nil {
		options.Discovery = &Discovery{Environment: winprofile.Environment{GOOS: "linux", Home: t.TempDir(), Root: t.TempDir(), Env: func(string) string { return "" }}, Now: time.Now, BrowserCredentials: true}
	}
	options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewClient(options)
}

func tokenWithExpiry(expiry int64) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"fixture","exp":%d}`, expiry))) + ".sig"
}

func fixtureDB(t *testing.T, path, schema string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func Test_Cursor_sources_fall_back_in_one_fetch_and_remember_rejection(t *testing.T) {
	// Given
	var rejectedCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != usageSummaryPath {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Cookie") == "WorkosCursorSessionToken=fixture%3A%3A"+tokenWithExpiry(0) {
			rejectedCalls++
			w.WriteHeader(401)
			return
		}
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.authPath = writeAuthFile(t, tokenWithExpiry(0))
	appPath := client.discovery.files(client.authPath)[1].path
	db := fixtureDB(t, appPath, `CREATE TABLE ItemTable(key TEXT, value TEXT)`)
	if _, err := db.Exec(`INSERT INTO ItemTable VALUES('cursorAuth/accessToken',?)`, jwtWithSub("app-user", "payload")); err != nil {
		t.Fatal(err)
	}
	client.SetDesktopCookie("desktop=fixture", "Firefox")
	// When
	first, err := client.Fetch(context.Background())
	second, secondErr := client.Fetch(context.Background())
	// Then
	if err != nil || secondErr != nil || first.Auth.Source == nil || first.Auth.Source.Kind != "app" || second.Auth.Source.Kind != "app" || rejectedCalls != 1 {
		t.Fatalf("fallback: %#v %#v calls=%d errors=%v/%v", first.Auth, second.Auth, rejectedCalls, err, secondErr)
	}
	if first.Auth.Checked[0].Status != "expired" {
		t.Fatal(first.Auth.Checked)
	}
}

func Test_Cursor_expired_JWT_skips_HTTP_and_pushed_expired_not_adopted(t *testing.T) {
	// Given
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
	t.Cleanup(srv.Close)
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client(), AuthPath: writeAuthFile(t, tokenWithExpiry(1))})
	// When
	if err := client.SetAccessToken(tokenWithExpiry(1)); err != nil {
		t.Fatal(err)
	}
	data, err := client.Fetch(context.Background())
	// Then
	if err != nil || calls != 0 || client.pushed.cookie != "" || data.Auth.State != "expired" || data.Auth.Source == nil || data.Auth.Source.Name != "cursor-agent" {
		t.Fatalf("expired: %#v calls=%d err=%v", data.Auth, calls, err)
	}
}

func Test_Cursor_changed_CLI_supersedes_active_desktop(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{})
	path := filepath.Join(t.TempDir(), "auth.json")
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client(), AuthPath: path})
	client.SetCookieHeader("desktop=fixture")
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"accessToken":%q}`, jwtWithSub("new-cli", "payload"))), 0o600); err != nil {
		t.Fatal(err)
	}
	// When
	data, err := client.Fetch(context.Background())
	// Then
	if err != nil || data.Auth.Source == nil || data.Auth.Source.Kind != "cli" {
		t.Fatalf("source=%#v err=%v", data.Auth, err)
	}
}

func Test_Cursor_auth_errors_and_sign_in_hints_follow_source(t *testing.T) {
	// Given
	cases := []struct {
		kind, name, message string
		command, url        bool
	}{
		{"", "", "Not signed in to Cursor. Run `cursor-agent login` or sign in to cursor.com in a browser.", true, true},
		{"cli", "cursor-agent", "Your cursor-agent login expired. Run `cursor-agent login` again.", true, false},
		{"app", "Cursor", "Your Cursor app sign-in expired. Open Cursor and sign in again.", false, true},
		{"browser", "Firefox", "Your Firefox sign-in to cursor.com expired. Sign in to cursor.com in Firefox again.", false, true},
		{"desktop", "Firefox", "The Firefox sign-in shared by Headroom expired. Sign in to cursor.com in Firefox again.", false, true},
		{"desktop", "browser", "The browser sign-in shared by Headroom expired. Sign in to cursor.com again.", false, true},
		{"api", "API", "The Cursor credential sent to the server expired. Send a new one.", false, true},
	}
	for _, tt := range cases {
		t.Run(tt.kind+tt.name, func(t *testing.T) {
			state := "expired"
			var source *usage.AuthSource
			if tt.kind == "" {
				state = "signed_out"
			} else {
				source = &usage.AuthSource{Kind: tt.kind, Name: tt.name}
			}
			// When
			auth := usage.NewAuth("Cursor", state, source)
			message := cursorAuthError(auth)
			// Then
			if message != tt.message || (auth.SignInCommand != nil) != tt.command || (auth.SignInURL != nil) != tt.url {
				t.Fatalf("auth=%#v error=%q", auth, message)
			}
		})
	}
}
