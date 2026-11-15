package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func testLockConfig() claudeCodeLockConfig {
	return claudeCodeLockConfig{
		maxAttempts:       5,
		retryWait:         0,
		jitter:            false,
		heartbeatInterval: 0,
		staleAge:          60 * time.Second,
	}
}

func Test_acquireClaudeCodeLock_creates_both_dirs_and_release_removes_them(t *testing.T) {
	// Given
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// When
	lock, err := acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
	// Then
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	newPath := filepath.Join(dir, claudeCodeLockName)
	legacyPath := dir + ".lock"
	if _, err := os.Lstat(newPath); err != nil {
		t.Fatalf("new lock missing: %v", err)
	}
	if _, err := os.Lstat(legacyPath); err != nil {
		t.Fatalf("legacy lock missing: %v", err)
	}
	lock.release()
	if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new lock still present: %v", err)
	}
	if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy lock still present: %v", err)
	}
}

func Test_acquireClaudeCodeLock_takes_stale_lock(t *testing.T) {
	// Given
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, claudeCodeLockName)
	if err := os.Mkdir(stale, 0o777); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// When
	lock, err := acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
	// Then
	if err != nil {
		t.Fatalf("acquire stale: %v", err)
	}
	defer lock.release()
	info, err := os.Lstat(stale)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("stale lock was not reclaimed: mtime=%v", info.ModTime())
	}
}

func Test_claudeCodeLock_heartbeatStep_advances_mtime_and_foreign_mtime_is_sticky_loss(t *testing.T) {
	// Given
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	path := filepath.Join(dir, claudeCodeLockName)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	lock.cfg.now = func() time.Time { return before.ModTime().Add(5 * time.Second) }
	// When: our heartbeat
	if err := lock.heartbeatStep(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Then
	if !after.ModTime().Equal(before.ModTime().Add(5 * time.Second)) {
		t.Fatalf("heartbeat mtime = %v, want %v", after.ModTime(), before.ModTime().Add(5*time.Second))
	}
	if lock.isLost() {
		t.Fatal("own heartbeat marked lock lost")
	}
	// When: foreign mtime
	foreign := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	if err := os.Chtimes(path, foreign, foreign); err != nil {
		t.Fatal(err)
	}
	if err := lock.heartbeatStep(); !errors.Is(err, errClaudeCodeLockLost) {
		t.Fatalf("foreign mtime error = %v", err)
	}
	if !lock.isLost() {
		t.Fatal("ownership loss must be sticky")
	}
	if err := lock.heartbeatStep(); !errors.Is(err, errClaudeCodeLockLost) {
		t.Fatalf("sticky loss cleared: %v", err)
	}
}

func Test_acquireClaudeCodeLock_permission_on_new_lock_is_not_writable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests skip when euid 0")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory permissions do not block creating the lock")
	}
	// Given: read-only parent so mkdir fails
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	// When
	_, err := acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
	// Then
	if !errors.Is(err, credstore.ErrNotWritable) {
		t.Fatalf("err = %v, want ErrNotWritable", err)
	}
}
