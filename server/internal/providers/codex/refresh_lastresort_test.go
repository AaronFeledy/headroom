package codex

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *logCapture) WithGroup(string) slog.Handler { return h }

func (h *logCapture) refused() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, record := range h.records {
		if record.Message == "credential refresh refused" {
			count++
		}
	}
	return count
}

func Test_Client_old_last_refresh_alone_does_not_refresh(t *testing.T) {
	// Given: a Codex sign-in with no expiry and an old last_refresh.
	path := writeAuth(t, `{"tokens":{"access_token":"access","refresh_token":"refresh","account_id":"acct"},"last_refresh":"2000-01-01T00:00:00Z"}`)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		assertWhamRequest(t, r, "access", "acct")
		writeRefreshFixture(t, w, `{"rate_limit":{"primary_window":{"used_percent":1}}}`)
	}))
	t.Cleanup(server.Close)
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch data=%+v err=%v", data, err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
}

func Test_Client_unauthorized_with_unchanged_pair_refreshes(t *testing.T) {
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-r","account_id":"acct"},"last_refresh":"2000-01-01T00:00:00Z"}`)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			writeRefreshFixture(t, w, `{"access_token":"new-access","refresh_token":"new-r","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeRefreshFixture(t, w, `{"rate_limit":{"primary_window":{"used_percent":1}}}`)
	}))
	t.Cleanup(server.Close)
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch data=%+v err=%v", data, err)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}
	if got := string(readFile(t, path)); !strings.Contains(got, `"access_token":"new-access"`) {
		t.Fatalf("refreshed tokens not persisted: %s", got)
	}
}

func Test_Client_unauthorized_adopts_changed_pair_without_exchange_despite_mtime(t *testing.T) {
	// Given
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-r","account_id":"acct"},"last_refresh":"2000-01-01T00:00:00Z"}`)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") == "Bearer old" {
			// Another writer replaced the sign-in without changing mtime.
			if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"adopted","refresh_token":"adopted-r","account_id":"acct"},"last_refresh":"2000-01-01T00:00:00Z"}`), 0o600); err != nil {
				t.Error(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer adopted" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeRefreshFixture(t, w, `{"rate_limit":{"primary_window":{"used_percent":1}}}`)
	}))
	t.Cleanup(server.Close)
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch data=%+v err=%v", data, err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0 while adopting", got)
	}
	if got := string(readFile(t, path)); !strings.Contains(got, `"access_token":"adopted"`) {
		t.Fatalf("adopted pair not kept: %s", got)
	}
}

func Test_Client_refusal_warns_once_per_episode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: an OpenCode sign-in with a passed expiry on a read-only file.
	path := writeAuth(t, `{"openai":{"access":"old","refresh":"old-r","accountId":"acct","expires":1}}`)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var unauthorized atomic.Bool
	unauthorized.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeRefreshFixture(t, w, `{"access_token":"new-access","refresh_token":"new-r","expires_in":3600}`)
			return
		}
		if unauthorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeRefreshFixture(t, w, `{"rate_limit":{"primary_window":{"used_percent":1}}}`)
	}))
	t.Cleanup(server.Close)
	capture := &logCapture{}
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage",
		TokenURL: server.URL + "/token", HTTPClient: server.Client(), Logger: slog.New(capture)})
	provider.store.source = CredentialSourceOpenCode

	// When: two polls are refused for the same spent pair.
	for range 2 {
		if _, err := provider.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// Then
	if got := capture.refused(); got != 1 {
		t.Fatalf("refusal records = %d, want 1", got)
	}

	// When: the file becomes writable; a poll saves a refreshed sign-in.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	unauthorized.Store(false)
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Then: a later refusal after the save starts a new episode.
	unauthorized.Store(true)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := capture.refused(); got != 2 {
		t.Fatalf("refusal records after reset = %d, want 2", got)
	}
}
