package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_CredentialStore_Refresh_writes_through_symlink_and_keeps_link(t *testing.T) {
	// Given
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "auth.json")
	writeFile(t, target, `{"tokens":{"access_token":"old","refresh_token":"old-refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	store := NewCredentialStore(CredentialOptions{Path: link})
	creds, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// When
	err = store.Refresh(context.Background(), creds, func(Credentials) (RefreshedTokens, error) {
		return RefreshedTokens{AccessToken: "new", RefreshToken: "new-refresh", AccountID: "acct", ExpiresAt: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), RefreshedAt: time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC)}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("auth path is no longer a symlink")
	}
	got := string(readFile(t, target))
	if !strings.Contains(got, `"access_token":"new"`) || !strings.Contains(got, `"refresh_token":"new-refresh"`) {
		t.Fatalf("target not updated: %s", got)
	}
	assertOwnerRestored(t, target)
	assertOwnerRestored(t, link)
}

func Test_CredentialStore_Refresh_adopts_changed_tokens_without_calling_exchange(t *testing.T) {
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	store := NewCredentialStore(CredentialOptions{Path: path})
	stale, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	writeFile(t, path, `{"tokens":{"access_token":"external","refresh_token":"external-refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	var exchanges atomic.Int32

	// When
	err = store.Refresh(context.Background(), stale, func(Credentials) (RefreshedTokens, error) {
		exchanges.Add(1)
		return RefreshedTokens{AccessToken: "new", RefreshToken: "new-refresh", AccountID: "acct", RefreshedAt: time.Now().UTC()}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := exchanges.Load(); got != 0 {
		t.Fatalf("exchange calls = %d, want 0", got)
	}
	got := string(readFile(t, path))
	if !strings.Contains(got, `"access_token":"external"`) || strings.Contains(got, `"access_token":"new"`) {
		t.Fatalf("file rewritten unexpectedly: %s", got)
	}
	assertOwnerRestored(t, path)
}

func Test_Client_proactive_refresh_keeps_token_when_store_unwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: an OpenCode-style expiry that has passed (NeedsRefresh) on a read-only file
	path := writeAuth(t, `{"openai":{"access":"access","refresh":"refresh","accountId":"acct","expires":1}}`)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, path)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assertWhamRequest(t, r, "access", "acct")
		writeRefreshFixture(t, w, `{"rate_limit":{"primary_window":{"used_percent":1},"secondary_window":{"used_percent":2}}}`)
	}))
	t.Cleanup(server.Close)
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	provider.store.source = CredentialSourceOpenCode

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch data=%+v err=%v", data, err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
	if data.NeedsReauth {
		t.Fatal("NeedsReauth = true, want false while keeping current token")
	}
	assertOwnerRestored(t, path)
}

func Test_Client_unauthorized_refresh_reports_expired_when_store_unwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, path)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			writeRefreshFixture(t, w, `{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0 (refused before exchange)", got)
	}
	if !data.NeedsReauth {
		t.Fatal("NeedsReauth = false, want true")
	}
	if data.Error == nil || !strings.Contains(*data.Error, "Headroom can't save a refreshed one because the credentials file is read-only") || !strings.Contains(*data.Error, "codex") {
		t.Fatalf("Error = %v, want unwritable message", data.Error)
	}
	assertOwnerRestored(t, path)
}

func Test_Client_doRefresh_succeeds_and_persists_valid_tokens_despite_body_close_error(t *testing.T) {
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	closeBoom := errors.New("close boom")
	body := &trackedBody{payload: `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`, closeErr: closeBoom}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/token" {
			return nil, errors.New("unexpected " + req.URL.Path)
		}
		return responseWithBody(http.StatusOK, body), nil
	})
	provider := New(Options{CredentialsPath: path, TokenURL: "https://codex.test/token", HTTPClient: &http.Client{Transport: transport}})
	creds, err := provider.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// When
	result, err := provider.doRefresh(context.Background(), creds)

	// Then
	if err != nil || result != usage.RefreshSuccess {
		t.Fatalf("doRefresh = %v, %v, want success", result, err)
	}
	got := string(readFile(t, path))
	if !strings.Contains(got, `"access_token":"new-access"`) || !strings.Contains(got, `"refresh_token":"new-refresh"`) {
		t.Fatalf("auth not persisted before close error: %s", got)
	}
	assertOwnerRestored(t, path)
}

func Test_CredentialStore_Refresh_returns_ErrNotWritable_without_exchange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-refresh","account_id":"acct"},"last_refresh":"2026-07-12T00:00:00Z"}`)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, path)
	store := NewCredentialStore(CredentialOptions{Path: path})
	stale, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var exchanges atomic.Int32

	// When
	err = store.Refresh(context.Background(), stale, func(Credentials) (RefreshedTokens, error) {
		exchanges.Add(1)
		return RefreshedTokens{AccessToken: "new", RefreshToken: "new-refresh", AccountID: "acct", RefreshedAt: time.Now().UTC()}, nil
	})

	// Then
	if !errors.Is(err, credstore.ErrNotWritable) {
		t.Fatalf("Refresh err = %v, want ErrNotWritable", err)
	}
	if got := exchanges.Load(); got != 0 {
		t.Fatalf("exchange calls = %d, want 0", got)
	}
	assertOwnerRestored(t, path)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func restoreWritebackMode(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
}

func writeRefreshFixture(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Error(err)
	}
}
