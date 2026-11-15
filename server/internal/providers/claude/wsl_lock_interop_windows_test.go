//go:build windows

package claude

// Windows-to-WSL lock interop fixture (opt-in). The Windows test binary drives
// the pinned Node contender inside WSL through wsl.exe against a directory the
// two kernels share over 9P (\\wsl.localhost\<distro>\tmp).
//
// Build and run from server/ (the Windows binary must run natively; WSL
// interop executes it on the host):
//
//	GOOS=windows GOARCH=amd64 go test -c -o /mnt/c/Users/<you>/AppData/Local/Temp/claude.test.exe ./internal/providers/claude
//	cd /mnt/c/Users/<you>/AppData/Local/Temp
//	WSLENV='HEADROOM_WSL_LOCK_FIXTURE:HEADROOM_WSL_NODE_PATH:HEADROOM_WSL_FIXTURE_SCRIPT:HEADROOM_WSL_DISTRO' \
//	HEADROOM_WSL_LOCK_FIXTURE=1 \
//	HEADROOM_WSL_NODE_PATH=/home/<you>/.nvm/versions/node/<version>/bin/node \
//	HEADROOM_WSL_FIXTURE_SCRIPT=/home/<you>/projects/headroom/server/internal/providers/claude/testdata/lockfile-interop/contender.js \
//	HEADROOM_WSL_DISTRO=Ubuntu \
//	./claude.test.exe -test.run WSL_lock_interop -test.v
//
// WSLENV is required: WSL does not forward the Linux shell environment to a
// native Windows process without it, and the /u flag excludes rather than
// includes a variable. The node path must be absolute because a non-login
// wsl.exe shell does not load nvm.
//
// The two kernels do not necessarily share a clock (WSL can drift ahead of the
// Windows host by seconds), so the fixture measures the offset and derives a
// staleness window larger than it. Staleness timing is the behavior under test.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func requireWSLFixture(t *testing.T) (argv []string, distro, winDir, linuxDir string) {
	t.Helper()
	if os.Getenv(wslLockFixtureEnv) != "1" {
		t.Skip("set " + wslLockFixtureEnv + "=1 to run the Windows-to-WSL lock fixture")
	}
	node := strings.TrimSpace(os.Getenv("HEADROOM_WSL_NODE_PATH"))
	script := strings.TrimSpace(os.Getenv("HEADROOM_WSL_FIXTURE_SCRIPT"))
	if node == "" || script == "" {
		t.Fatal("HEADROOM_WSL_NODE_PATH and HEADROOM_WSL_FIXTURE_SCRIPT are required")
	}
	distro = strings.TrimSpace(os.Getenv("HEADROOM_WSL_DISTRO"))
	if distro == "" {
		distro = "Ubuntu"
	}
	base := fmt.Sprintf("headroom-lock-interop-%d", os.Getpid())
	winDir = filepath.Join(`\\wsl.localhost\`+distro, "tmp", base)
	linuxDir = "/tmp/" + base
	if err := os.MkdirAll(winDir, 0o700); err != nil {
		t.Fatalf("create shared fixture directory %s: %v", winDir, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(winDir); err != nil {
			t.Errorf("remove fixture directory: %v", err)
		}
	})
	return []string{"wsl.exe", "-d", distro, "--", node, script}, distro, winDir, linuxDir
}

// wslStaleWindow measures the clock offset between this process and the WSL
// contender, then returns a staleness window comfortably larger than it.
func wslStaleWindow(t *testing.T, argv []string) time.Duration {
	t.Helper()
	linuxNow, err := strconv.ParseInt(runContender(t, argv, "clock"), 10, 64)
	if err != nil {
		t.Fatalf("parse contender clock: %v", err)
	}
	skew := time.Duration(linuxNow-time.Now().UnixMilli()) * time.Millisecond
	if skew < 0 {
		skew = -skew
	}
	stale := skew + 4*time.Second
	stale = (stale + time.Second - 1) / time.Second * time.Second
	if stale < 2*time.Second {
		stale = 2 * time.Second
	}
	return stale
}

func staleArg(stale time.Duration) string {
	return strconv.FormatInt(stale.Milliseconds(), 10)
}

func wslRun(t *testing.T, distro string, args ...string) {
	t.Helper()
	command := exec.Command("wsl.exe", append([]string{"-d", distro, "--"}, args...)...)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("wsl %v: %v: %s", args, err, out)
	}
}

func Test_WSL_lock_interop_mutual_exclusion(t *testing.T) {
	argv, _, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	holder := startContender(t, argv, "hold", linuxDir, staleArg(stale), "1000")
	holder.expect("LOCKED")
	if _, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 0)); !errors.Is(err, errClaudeCodeRefreshing) {
		t.Fatalf("Headroom acquired a lock the WSL contender holds: %v", err)
	}
	holder.release()

	lock, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 0))
	if err != nil {
		t.Fatalf("Headroom acquire over 9P: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()
	for _, name := range []string{"new", "legacy"} {
		if got := runContender(t, argv, "try", linuxDir, staleArg(stale), "1000", name); got != "ELOCKED:"+name {
			t.Fatalf("WSL contender %s lock = %q, want ELOCKED:%s", name, got, name)
		}
	}
}

func Test_WSL_lock_interop_heartbeat_visible_on_linux(t *testing.T) {
	argv, _, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	lock, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 250*time.Millisecond))
	if err != nil {
		t.Fatalf("Headroom acquire over 9P: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	// Past the staleness window, only the heartbeat written by the Windows
	// process keeps the lock fresh for a Linux reader.
	time.Sleep(stale + time.Second)
	if got := runContender(t, argv, "check", linuxDir, staleArg(stale), "new"); got != "FRESH" {
		t.Fatalf("WSL contender check after the stale window = %q, want FRESH", got)
	}
	if got := runContender(t, argv, "try", linuxDir, staleArg(stale), "1000", "new"); got != "ELOCKED:new" {
		t.Fatalf("WSL contender reclaimed a heartbeating lock: %q", got)
	}
}

func Test_WSL_lock_interop_legacy_lock_through_symlinked_config_dir(t *testing.T) {
	argv, distro, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	realWin := filepath.Join(winDir, "real-config")
	configWin := filepath.Join(winDir, "config")
	if err := os.MkdirAll(realWin, 0o700); err != nil {
		t.Fatalf("create real config dir: %v", err)
	}
	// 9P refuses symlink creation from Windows, so create the Linux symlink the
	// way a Linux user would and let Windows resolve it over the share.
	wslRun(t, distro, "ln", "-s", linuxDir+"/real-config", linuxDir+"/config")
	info, err := os.Lstat(configWin)
	if err != nil {
		t.Fatalf("stat config symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// The 9P redirector marks the Linux symlink as a reparse point but does
		// not expose its target, so a native Windows process cannot resolve it.
		t.Skipf("9P hides Linux symlink targets from Windows, so the legacy lock path cannot match Claude Code's realpath for a symlinked WSL config dir")
	}
	lock, err := acquireClaudeCodeLock(context.Background(), configWin, interopLockConfig(stale, 0))
	if err != nil {
		t.Fatalf("Headroom acquire through a symlinked config dir: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	// The legacy lock is created beside the resolved directory, matching Claude
	// Code's realpath(configDir).lock, not beside the symlink spelling.
	if _, err := os.Lstat(filepath.Join(realWin) + ".lock"); err != nil {
		t.Fatalf("legacy lock missing beside the resolved directory: %v", err)
	}
	if got := runContender(t, argv, "try", linuxDir+"/config", staleArg(stale), "1000", "legacy"); got != "ELOCKED:legacy" {
		t.Fatalf("WSL contender legacy lock = %q, want ELOCKED:legacy", got)
	}
}

func Test_WSL_lock_interop_credstore_replaces_file_over_9p(t *testing.T) {
	_, _, winDir, _ := requireWSLFixture(t)
	path := filepath.Join(winDir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatalf("write fixture credentials: %v", err)
	}
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		if string(current) != `{"token":"old"}` {
			t.Errorf("prepare saw %s", current)
		}
		return func(latest []byte) ([]byte, error) {
			return []byte(`{"token":"new"}`), nil
		}, nil
	})
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		t.Skipf("9P does not support LockFileEx, so a native Windows refresh cannot lock the WSL home yet: %v", err)
	}
	if err != nil {
		t.Fatalf("credstore.Refresh over 9P: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replaced credentials: %v", err)
	}
	if string(data) != `{"token":"new"}` {
		t.Fatalf("credentials = %s, want the replacement", data)
	}
	entries, err := os.ReadDir(winDir)
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp artifact remains after 9P replacement: %s", entry.Name())
		}
	}
}

func Test_WSL_lock_interop_timestamp_precision(t *testing.T) {
	argv, _, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	lock, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 0))
	if err != nil {
		t.Fatalf("Headroom acquire over 9P: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()
	// 9P may report a coarser mtime than the claim used; ownership must survive a
	// heartbeat renew and stay visible to a Linux reader.
	if err := lock.heartbeatStep(); err != nil {
		t.Fatalf("heartbeat over 9P: %v", err)
	}
	if got := runContender(t, argv, "check", linuxDir, staleArg(stale), "new"); got != "FRESH" {
		t.Fatalf("WSL contender check = %q, want FRESH", got)
	}
}

func Test_WSL_lock_interop_headroom_reclaims_stale_node_lock(t *testing.T) {
	argv, _, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	holder := startContender(t, argv, "hold-stale", linuxDir, staleArg(stale), "1000")
	holder.expect("LOCKED_STALE")
	// The contender stopped renewing its mtime; the Linux clock may run ahead,
	// so wait until this process's clock also considers the lock stale.
	waitForStale(t, filepath.Join(winDir, claudeCodeLockName), stale)

	lock, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 0))
	if err != nil {
		t.Fatalf("Headroom did not reclaim a stale WSL contender lock: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	holder.release()
}

func Test_WSL_lock_interop_node_reclaims_stale_headroom_lock(t *testing.T) {
	argv, _, winDir, linuxDir := requireWSLFixture(t)
	stale := wslStaleWindow(t, argv)

	lock, err := acquireClaudeCodeLock(context.Background(), winDir, interopLockConfig(stale, 0))
	if err != nil {
		t.Fatalf("Headroom acquire over 9P: %v", err)
	}
	// Wait until the contender's own staleness rule calls the lock stale.
	deadline := time.Now().Add(2*stale + time.Second)
	for runContender(t, argv, "check", linuxDir, staleArg(stale), "new") != "STALE" {
		if time.Now().After(deadline) {
			t.Fatal("Headroom lock never became stale for the WSL contender")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := runContender(t, argv, "try", linuxDir, staleArg(stale), "1000", "new"); got != "ACQUIRED" {
		t.Fatalf("WSL contender did not reclaim a stale Headroom lock: %q", got)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}
