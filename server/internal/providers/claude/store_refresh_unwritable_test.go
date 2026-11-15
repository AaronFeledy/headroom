package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func restoreWritebackMode(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
}

func Test_Client_file_refresh_unwritable_file_expires_without_token_request(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "r", ExpiresAt: pastMillis(t)})
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" || r.Method == http.MethodPost {
			atomic.AddInt32(&tokenHits, 1)
			t.Error("must not POST refresh when store is unwritable")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig()
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&tokenHits) != 0 {
		t.Fatalf("token hits = %d", tokenHits)
	}
	if got.Auth.State != "expired" || !got.NeedsReauth {
		t.Fatalf("auth = %+v", got.Auth)
	}
	if got.Auth.SignInCommand == nil || got.ReauthCommand == nil {
		t.Fatal("sign-in command must be kept")
	}
	want := "Sign-in expired, and Headroom can't save a refreshed one because the credentials file is read-only. Use Claude Code once, or run `"
	if got.Error == nil || !strings.Contains(*got.Error, want) {
		t.Fatalf("error = %v", got.Error)
	}
	if strings.Contains(strings.ToLower(*got.Error), "refresh token") {
		t.Fatalf("message must not expose token internals: %s", *got.Error)
	}
}

func Test_Client_opencode_unwritable_file_uses_opencode_copy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	// Given
	path := writeOpenCodeCredentials(t, credentialFixture{Access: "old", Refresh: "r", ExpiresAt: pastMillis(t)})
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt32(&tokenHits, 1)
			t.Error("must not POST refresh when OpenCode store is unwritable")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&tokenHits) != 0 {
		t.Fatalf("token hits = %d", tokenHits)
	}
	if got.Auth.State != "expired" || got.Error == nil {
		t.Fatalf("got = %+v", got)
	}
	if !strings.Contains(*got.Error, "because the credentials file is read-only. Use OpenCode once, or run `claude auth login`.") {
		t.Fatalf("error = %q", *got.Error)
	}
}

func Test_Client_unwritable_proactive_keeps_unexpired_token(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	home, path := claudeHomeLayout(t)
	future := time.Now().UTC().Add(30 * time.Second)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "a", Refresh: "r", ExpiresAt: future.UnixMilli()})
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	restoreWritebackMode(t, path)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer a" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig()
	got, err := client.Fetch(context.Background())
	if err != nil || got.Error != nil || got.Auth.State != "signed_in" || tokenCalls.Load() != 0 {
		t.Fatalf("Fetch = %+v, %v; token calls = %d", got, err, tokenCalls.Load())
	}
}
