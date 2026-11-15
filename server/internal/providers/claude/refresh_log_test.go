package claude

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

func (h *recordHandler) refused() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var records []slog.Record
	for _, record := range h.records {
		if record.Message == "credential refresh refused" {
			records = append(records, record)
		}
	}
	return records
}

func Test_Client_refusal_warns_once_per_episode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: an expired sign-in on a read-only file.
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var unauthorized atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeResponse(t, w, `{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)
			return
		}
		if unauthorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	handler := &recordHandler{}
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/usage",
		TokenURL: server.URL + "/token", HTTPClient: server.Client(), Logger: slog.New(handler)})
	client.lockConfig = testLockConfig()

	// When: two polls are refused for the same spent pair.
	for range 2 {
		if _, err := client.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// Then: one warning names the path and reason.
	refused := handler.refused()
	if len(refused) != 1 {
		t.Fatalf("refusal records = %d, want 1", len(refused))
	}
	assertRecordAttr(t, refused[0], "path", path)
	assertRecordAttr(t, refused[0], "reason", string(credstore.ReasonReadOnlyFile))

	// When: the file becomes writable and a poll saves a refreshed sign-in.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Then: a later refusal after the save starts a new episode.
	unauthorized.Store(true)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(handler.refused()); got != 2 {
		t.Fatalf("refusal records after reset = %d, want 2", got)
	}
}

func assertRecordAttr(t *testing.T, record slog.Record, key, want string) {
	t.Helper()
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			found = true
			if attr.Value.String() != want {
				t.Fatalf("%s = %q, want %q", key, attr.Value.String(), want)
			}
			return false
		}
		return true
	})
	if !found {
		t.Fatalf("record has no %s attribute", key)
	}
}
