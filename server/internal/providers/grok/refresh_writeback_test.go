package grok

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func Test_Provider_refresh_writes_through_symlink_and_keeps_link(t *testing.T) {
	// Given
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "auth.json")
	writeFileContents(t, target, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			tokenCalls.Add(1)
			writeRefreshFixture(t, w, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: link,
		HTTPClient:      server.Client(),
		TokenURL:        server.URL + "/oauth2/token",
		Now:             func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	stale, err := parseCredentials([]byte(`{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// When
	_, err = provider.refresh(context.Background(), stale, refreshReasonUnauthorized)

	// Then
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("credentials path is no longer a symlink")
	}
	got := string(mustRead(t, target))
	if !strings.Contains(got, `"new-token"`) || !strings.Contains(got, `"new-refresh"`) {
		t.Fatalf("target not updated: %s", got)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}
	assertOwnerRestored(t, target)
	assertOwnerRestored(t, link)
}

func Test_Provider_refresh_does_not_rewrite_when_tokens_already_changed(t *testing.T) {
	// Given
	const staleJSON = `{"https://auth.x.ai/oauth2/token":{"key":"stale-token","refresh_token":"stale-refresh","expires_at":1}}`
	const rotatedJSON = `{"https://auth.x.ai/oauth2/token":{"key":"rotated-token","refresh_token":"rotated-refresh","expires_at":1800000000000,"marker":"keep"}}`
	authPath := writeAuthFile(t, staleJSON)
	stale, err := parseCredentials([]byte(staleJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := os.WriteFile(authPath, []byte(rotatedJSON), 0o600); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
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

	// When
	adopted, err := provider.refresh(context.Background(), stale, refreshReasonUnauthorized)

	// Then
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
	if adopted.accessToken != "rotated-token" || adopted.refreshToken != "rotated-refresh" {
		t.Fatalf("adopted = %q/%q", adopted.accessToken, adopted.refreshToken)
	}
	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("file rewritten: mtime/size changed")
	}
	if !strings.Contains(string(mustRead(t, authPath)), `"marker":"keep"`) {
		t.Fatal("marker lost")
	}
	assertOwnerRestored(t, authPath)
}

func writeFileContents(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

func writeRefreshFixture(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Error(err)
	}
}
