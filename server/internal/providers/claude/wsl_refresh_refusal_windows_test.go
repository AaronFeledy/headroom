//go:build windows

package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_WSL_lock_interop_credstore_refuses_unlockable_store(t *testing.T) {
	_, winDir, _ := requireWSLDir(t)
	path := filepath.Join(winDir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatalf("write fixture credentials: %v", err)
	}
	prepared := false
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		prepared = true
		return nil, errors.New("prepare must not run")
	})
	var refused *credstore.NotWritableError
	if !errors.As(err, &refused) {
		t.Fatalf("Refresh over 9P = %v, want NotWritableError", err)
	}
	if refused.Reason != credstore.ReasonLockUnsupported {
		t.Fatalf("reason = %q, want %q", refused.Reason, credstore.ReasonLockUnsupported)
	}
	if prepared {
		t.Fatal("prepare ran before the lock refusal")
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != `{"token":"old"}` {
		t.Fatalf("credentials changed: %s, %v", data, readErr)
	}
}

func Test_WSL_lock_interop_claude_refuses_wsl_home_refresh(t *testing.T) {
	_, winDir, _ := requireWSLDir(t)
	home := t.TempDir()
	dir := filepath.Join(winDir, ".claude")
	path := writeClaudeCredentialsIn(t, filepath.Join(dir, ".credentials.json"), credentialFixture{
		Access: "old-access", Refresh: "old-refresh", ExpiresAt: pastMillis(t),
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(Options{
		HomeDir:    home,
		WSLHomeDir: winDir,
		UsageURL:   server.URL + "/usage",
		TokenURL:   server.URL + "/token",
		HTTPClient: server.Client(),
	})
	client.lockConfig = testLockConfig()

	got, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if requests != 0 {
		t.Fatalf("upstream requests = %d, want none before the lock refusal", requests)
	}
	if got.Auth.State != "expired" || !got.NeedsReauth || got.Error == nil {
		t.Fatalf("auth=%+v error=%v", got.Auth, got.Error)
	}
	want := "because Windows can't lock files in this location."
	if !strings.Contains(*got.Error, want) {
		t.Fatalf("error = %q, want %q", *got.Error, want)
	}
	if got.Auth.Source == nil || got.Auth.Source.Name != "Claude Code (WSL)" {
		t.Fatalf("source = %+v, want the WSL Claude Code candidate", got.Auth.Source)
	}
	if data := readFile(t, path); !strings.Contains(data, `"accessToken":"old-access"`) {
		t.Fatalf("credentials changed: %s", data)
	}
	// Claude Code's mkdir lock is taken before the refusal and must be released.
	for _, lockPath := range []string{filepath.Join(dir, claudeCodeLockName), dir + ".lock"} {
		if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock left behind at %s: %v", lockPath, err)
		}
	}
}
