package grok

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type grokLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *grokLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *grokLogCapture) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *grokLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *grokLogCapture) WithGroup(string) slog.Handler { return h }

func (h *grokLogCapture) refused() int {
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

func Test_Provider_does_not_refresh_inside_former_buffer(t *testing.T) {
	// Given: the token expires in four minutes and now is fixed.
	now := time.Unix(1700000000, 0).UTC()
	expiresAt := now.Add(4 * time.Minute).UnixMilli()
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":`+strconv.FormatInt(expiresAt, 10)+`}}`)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenCalls.Add(1)
		case "/v1/billing":
			writeRefreshFixture(t, w, `{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`)
		case "/v1/settings":
			writeRefreshFixture(t, w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: authPath,
		HTTPClient:      server.Client(),
		BillingURL:      server.URL + "/v1/billing",
		SettingsURL:     server.URL + "/v1/settings",
		WebBillingURL:   server.URL + "/web",
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch data=%+v err=%v", data, err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0 before the effective expiry", got)
	}
}

func Test_Provider_refusal_warns_once_per_episode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: an expired sign-in on a read-only file.
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	if err := os.Chmod(authPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, authPath)
	var unauthorized atomic.Bool
	unauthorized.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			writeRefreshFixture(t, w, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`)
		case "/v1/billing":
			if unauthorized.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeRefreshFixture(t, w, `{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`)
		case "/v1/settings":
			writeRefreshFixture(t, w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	capture := &grokLogCapture{}
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: authPath,
		HTTPClient:      server.Client(),
		BillingURL:      server.URL + "/v1/billing",
		SettingsURL:     server.URL + "/v1/settings",
		WebBillingURL:   server.URL + "/web",
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
		Logger:          slog.New(capture),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

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

	// When: the file becomes writable and a poll saves a refreshed sign-in.
	if err := os.Chmod(authPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	unauthorized.Store(false)
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Then: a later refusal after the save starts a new episode.
	unauthorized.Store(true)
	if err := os.Chmod(authPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := capture.refused(); got != 2 {
		t.Fatalf("refusal records after reset = %d, want 2", got)
	}
}
