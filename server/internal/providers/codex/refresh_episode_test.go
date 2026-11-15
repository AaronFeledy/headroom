package codex

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func Test_Client_failed_refresh_keeps_the_refusal_episode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given: an expired OpenCode sign-in on a read-only file, already reported once.
	path := writeAuth(t, `{"openai":{"access":"old","refresh":"old-r","accountId":"acct","expires":1}}`)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenRequests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	capture := &logCapture{}
	provider := New(Options{CredentialsPath: path, UsageURL: server.URL + "/usage",
		TokenURL: server.URL + "/token", HTTPClient: server.Client(), Logger: slog.New(capture)})
	provider.store.source = CredentialSourceOpenCode
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// When: the file becomes writable but the refresh fails, then read-only again.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Then: nothing was saved, so the same refusal is not reported twice.
	if tokenRequests.Load() == 0 {
		t.Fatal("the writable poll never reached the token endpoint")
	}
	if got := capture.refused(); got != 1 {
		t.Fatalf("refusal records = %d, want 1", got)
	}
}
