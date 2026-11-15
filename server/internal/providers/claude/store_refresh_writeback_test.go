package claude

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
)

func claudeHomeLayout(t *testing.T) (home, credPath string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credPath = filepath.Join(dir, ".credentials.json")
	return home, credPath
}

func testClientLockConfig(hook func(context.Context, int)) claudeCodeLockConfig {
	cfg := testLockConfig()
	cfg.waitHook = hook
	return cfg
}

func Test_Client_file_refresh_writes_through_symlink(t *testing.T) {
	// Given
	home, target := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, target, credentialFixture{Access: "old", Refresh: "old-refresh", ExpiresAt: pastMillis(t)})
	linkDir := filepath.Join(t.TempDir(), "link-home", ".claude")
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, ".credentials.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenHits, 1)
			assertRefreshRequest(t, r, "old-refresh")
			writeResponse(t, w, `{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: link, HomeDir: home, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig()
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatalf("fetch: %+v %v", got, err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %#v %v", info, err)
	}
	body := compactJSON(t, readFile(t, target))
	if !strings.Contains(body, `"accessToken":"new"`) || !strings.Contains(body, `"refreshToken":"new-refresh"`) {
		t.Fatalf("target not updated: %s", body)
	}
	if atomic.LoadInt32(&tokenHits) != 1 {
		t.Fatalf("token hits = %d", tokenHits)
	}
}

func Test_Client_file_refresh_holds_claude_code_locks_during_token_handler(t *testing.T) {
	// Given
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "r", ExpiresAt: pastMillis(t)})
	dir := filepath.Dir(path)
	var sawNew, sawLegacy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if _, err := os.Lstat(filepath.Join(dir, claudeCodeLockName)); err == nil {
				sawNew.Store(true)
			}
			if _, err := os.Lstat(dir + ".lock"); err == nil {
				sawLegacy.Store(true)
			}
			writeResponse(t, w, `{"access_token":"n","refresh_token":"nr","expires_in":3600}`)
			return
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig()
	// When
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Then
	if !sawNew.Load() || !sawLegacy.Load() {
		t.Fatalf("locks during token: new=%v legacy=%v", sawNew.Load(), sawLegacy.Load())
	}
	if _, err := os.Lstat(filepath.Join(dir, claudeCodeLockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new lock left behind: %v", err)
	}
	if _, err := os.Lstat(dir + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy lock left behind: %v", err)
	}
}

func Test_Client_file_refresh_waits_on_foreign_lock_and_adopts_without_token_request(t *testing.T) {
	// Given
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	dir := filepath.Dir(path)
	foreign := filepath.Join(dir, claudeCodeLockName)
	if err := os.Mkdir(foreign, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreign, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenHits, 1)
			t.Error("must not refresh when foreign lock finishes first")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer adopted" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeResponse(t, w, `{"five_hour":{"utilization":3}}`)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testClientLockConfig(func(ctx context.Context, attempt int) {
		writeClaudeCredentialsAt(t, path, credentialFixture{Access: "adopted", Refresh: "adopted-r", ExpiresAt: futureMillis(t)})
		if err := removeLockDir(foreign); err != nil {
			t.Fatal(err)
		}
	})
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatalf("fetch: %+v %v", got, err)
	}
	if atomic.LoadInt32(&tokenHits) != 0 {
		t.Fatalf("token hits = %d", tokenHits)
	}
	if !strings.Contains(readFile(t, path), `"accessToken":"adopted"`) {
		t.Fatal("did not keep adopted tokens")
	}
}

func Test_Client_file_refresh_foreign_lock_never_released_is_transient(t *testing.T) {
	// Given
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	dir := filepath.Dir(path)
	foreign := filepath.Join(dir, claudeCodeLockName)
	if err := os.Mkdir(foreign, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreign, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenHits, 1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig() // waitHook nil, retryWait 0, 5 busy attempts
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "Token refresh failed. Will retry.") {
		t.Fatalf("error = %v", got.Error)
	}
	if atomic.LoadInt32(&tokenHits) != 0 {
		t.Fatalf("token hits = %d", tokenHits)
	}
}

func Test_Client_file_refresh_lost_lock_before_prepare_skips_token_request(t *testing.T) {
	// Given
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "r", ExpiresAt: pastMillis(t)})
	dir := filepath.Dir(path)
	var tokenHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" || r.Method == http.MethodPost {
			atomic.AddInt32(&tokenHits, 1)
			t.Error("token must not run after lock loss")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(Options{HomeDir: home, CredentialsPath: path, UsageURL: server.URL + "/u", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	cfg := testLockConfig()
	cfg.afterAcquire = func(lock *claudeCodeLock) {
		foreign := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Millisecond)
		if err := os.Chtimes(filepath.Join(dir, claudeCodeLockName), foreign, foreign); err != nil {
			t.Fatal(err)
		}
		if err := lock.heartbeatStep(); !errors.Is(err, errClaudeCodeLockLost) {
			t.Fatalf("heartbeat = %v, want lost ownership", err)
		}
	}
	client.lockConfig = cfg
	// When
	got, err := client.Fetch(context.Background())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&tokenHits) != 0 {
		t.Fatalf("token hits = %d", tokenHits)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "Token refresh failed. Will retry.") {
		t.Fatalf("error = %v", got.Error)
	}
}
