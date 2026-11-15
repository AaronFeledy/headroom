package grok

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Provider_proactive_refresh_keeps_token_when_store_unwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	if err := os.Chmod(authPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, authPath)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
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
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
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
		t.Fatalf("token calls = %d, want 0", got)
	}
	if data.NeedsReauth {
		t.Fatal("NeedsReauth = true, want false")
	}
	assertOwnerRestored(t, authPath)
}

func Test_Provider_unauthorized_refresh_reports_expired_when_store_unwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: fresh token so no proactive refresh; billing returns 401
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1800000000000}}`)
	if err := os.Chmod(authPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, authPath)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenCalls.Add(1)
			writeRefreshFixture(t, w, `{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`)
		case "/v1/billing":
			w.WriteHeader(http.StatusUnauthorized)
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
		TokenURL:        server.URL + "/oauth2/token",
		WebBillingURL:   server.URL + "/web",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
	if !data.NeedsReauth {
		t.Fatal("NeedsReauth = false, want true")
	}
	if data.Error == nil || !strings.Contains(*data.Error, "Headroom can't save a refreshed one because the credentials file is read-only") || !strings.Contains(*data.Error, "grok login") {
		t.Fatalf("Error = %v, want unwritable message", data.Error)
	}
	assertOwnerRestored(t, authPath)
}

func Test_Provider_refresh_returns_ErrNotWritable_without_token_request(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	if err := os.Chmod(authPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restoreWritebackMode(t, authPath)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: authPath,
		HTTPClient:      server.Client(),
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	stale, err := parseCredentials(mustRead(t, authPath))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When
	_, err = provider.refresh(context.Background(), stale, refreshReasonUnauthorized)

	// Then
	if !errors.Is(err, credstore.ErrNotWritable) {
		t.Fatalf("refresh err = %v, want ErrNotWritable", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
	assertOwnerRestored(t, authPath)
}

func restoreWritebackMode(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
}
