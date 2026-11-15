package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func Test_acquireClaudeCodeLock_keeps_foreign_files_symlinks_and_nonempty_dirs(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "nonempty"} {
		t.Run(kind, func(t *testing.T) {
			home, path := claudeHomeLayout(t)
			dir := filepath.Dir(path)
			lockPath := filepath.Join(dir, claudeCodeLockName)
			switch kind {
			case "file":
				if err := os.WriteFile(lockPath, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(home, lockPath); err != nil {
					t.Fatal(err)
				}
			case "nonempty":
				if err := os.Mkdir(lockPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(lockPath, "foreign"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-2 * time.Minute)
				if err := os.Chtimes(lockPath, old, old); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
			if !errors.Is(err, errClaudeCodeRefreshing) {
				t.Fatalf("acquire = %v", err)
			}
			after, err := os.Lstat(lockPath)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("foreign path changed: %v", err)
			}
		})
	}
}

func Test_acquireClaudeCodeLock_releases_new_lock_while_legacy_is_busy(t *testing.T) {
	_, path := claudeHomeLayout(t)
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := testLockConfig()
	waits := 0
	cfg.waitHook = func(context.Context, int) {
		waits++
		if _, err := os.Lstat(filepath.Join(dir, claudeCodeLockName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new lock retained while waiting for legacy: %v", err)
		}
		if err := removeLockDir(dir + ".lock"); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := acquireClaudeCodeLock(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Fatalf("waits = %d, want 1", waits)
	}
}

func Test_claudeCodeLock_release_keeps_replaced_directory_with_same_mtime(t *testing.T) {
	_, path := claudeHomeLayout(t)
	dir := filepath.Dir(path)
	lock, err := acquireClaudeCodeLock(context.Background(), dir, testLockConfig())
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, claudeCodeLockName)
	before, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(lockPath, lockPath+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	lock.cfg.now = func() time.Time { return before.ModTime().Add(5 * time.Second) }
	if err := lock.heartbeatStep(); !errors.Is(err, errClaudeCodeLockLost) {
		t.Fatalf("heartbeat accepted a replacement inode: %v", err)
	}
	after, err := os.Lstat(lockPath)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("heartbeat touched a foreign directory: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("release removed foreign directory: %v", err)
	}
}
