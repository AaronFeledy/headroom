package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_Client_refresh_commits_after_poll_cancellation_or_lock_loss(t *testing.T) {
	for _, event := range []string{"cancel", "lock_lost"} {
		t.Run(event, func(t *testing.T) {
			home, path := claudeHomeLayout(t)
			writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			held := make(chan *claudeCodeLock, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if event == "cancel" {
					cancel()
				} else {
					foreign := time.Now().Add(time.Hour)
					if err := os.Chtimes(filepath.Join(filepath.Dir(path), claudeCodeLockName), foreign, foreign); err != nil {
						t.Error(err)
					}
					var lock *claudeCodeLock
					select {
					case lock = <-held:
					case <-r.Context().Done():
						t.Error("token request started before the lock was acquired")
						return
					}
					if err := lock.heartbeatStep(); !errors.Is(err, errClaudeCodeLockLost) {
						t.Errorf("heartbeat = %v, want lost ownership", err)
					}
				}
				writeResponse(t, w, `{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)
			}))
			defer server.Close()
			client := New(Options{HomeDir: home, CredentialsPath: path, TokenURL: server.URL, HTTPClient: server.Client()})
			client.lockConfig = testLockConfig()
			client.lockConfig.afterAcquire = func(lock *claudeCodeLock) { held <- lock }
			creds, err := client.loadCredentials(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.refreshCredentials(ctx, creds, refreshModeForced); err != nil {
				t.Fatal(err)
			}
			current, err := client.currentCredentials(context.Background())
			if err != nil || current.accessToken != "new" || current.refreshToken != "new-r" {
				t.Fatalf("reloaded credentials = %+v, %v", current, err)
			}
			body := compactJSON(t, readFile(t, path))
			if !strings.Contains(body, `"accessToken":"new"`) || !strings.Contains(body, `"refreshToken":"new-r"`) {
				t.Fatalf("rotated tokens not persisted: %s", body)
			}
		})
	}
}

type refreshCloseErrorBody struct {
	io.Reader
	err error
}

func (b refreshCloseErrorBody) Close() error { return b.err }

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func Test_Client_refresh_persists_tokens_and_succeeds_when_body_close_fails(t *testing.T) {
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	closeErr := errors.New("fixture close failure")
	transport := refreshTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: refreshCloseErrorBody{
			Reader: strings.NewReader(`{"access_token":"new","refresh_token":"new-r","expires_in":3600}`), err: closeErr,
		}}, nil
	})
	client := New(Options{HomeDir: home, CredentialsPath: path, TokenURL: "https://fixture.test/token", HTTPClient: &http.Client{Transport: transport}})
	client.lockConfig = testLockConfig()
	creds, err := client.loadCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = client.refreshCredentials(context.Background(), creds, refreshModeForced)
	if err != nil {
		t.Fatalf("refresh = %v, want success", err)
	}
	current, err := client.currentCredentials(context.Background())
	if err != nil || current.accessToken != "new" || current.refreshToken != "new-r" {
		t.Fatalf("reloaded credentials = %+v, %v", current, err)
	}
	if !strings.Contains(compactJSON(t, readFile(t, path)), `"refreshToken":"new-r"`) {
		t.Fatal("rotated refresh token not persisted")
	}
}

func Test_Client_refresh_preserves_either_token_changed_during_exchange(t *testing.T) {
	for _, changed := range []string{"access", "refresh"} {
		t.Run(changed, func(t *testing.T) {
			home, path := claudeHomeLayout(t)
			writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
			wantAccess, wantRefresh := "old", "old-r"
			if changed == "access" {
				wantAccess = "external"
			} else {
				wantRefresh = "external-r"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeClaudeCredentialsAt(t, path, credentialFixture{Access: wantAccess, Refresh: wantRefresh, ExpiresAt: futureMillis(t)})
				writeResponse(t, w, `{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)
			}))
			defer server.Close()
			client := New(Options{HomeDir: home, CredentialsPath: path, TokenURL: server.URL, HTTPClient: server.Client()})
			client.lockConfig = testLockConfig()
			stale, err := client.loadCredentials(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := client.refreshCredentials(context.Background(), stale, refreshModeForced); err != nil {
				t.Fatal(err)
			}
			current, err := client.currentCredentials(context.Background())
			if err != nil || current.accessToken != wantAccess || current.refreshToken != wantRefresh {
				t.Fatalf("concurrent sign-in replaced: %+v, %v", current, err)
			}
		})
	}
}

func Test_Client_refresh_adopts_changed_tokens_when_preflight_is_unwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	home, path := claudeHomeLayout(t)
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "old", Refresh: "old-r", ExpiresAt: pastMillis(t)})
	foreign := filepath.Join(filepath.Dir(path), claudeCodeLockName)
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	client := New(Options{HomeDir: home, CredentialsPath: path, HTTPClient: &http.Client{Transport: refreshTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unwritable refresh must not exchange tokens")
		return nil, errors.New("unexpected token exchange")
	})}})
	client.lockConfig = testClientLockConfig(func(context.Context, int) {
		writeClaudeCredentialsAt(t, path, credentialFixture{Access: "adopted", Refresh: "adopted-r", ExpiresAt: futureMillis(t)})
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if err := removeLockDir(foreign); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
	stale, err := client.loadCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.refreshCredentials(context.Background(), stale, refreshModeForced); err != nil {
		t.Fatal(err)
	}
	current, err := client.currentCredentials(context.Background())
	if err != nil || current.accessToken != "adopted" || current.refreshToken != "adopted-r" {
		t.Fatalf("unwritable store did not adopt external refresh: %+v, %v", current, err)
	}
}
