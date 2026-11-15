package claude

// proper-lockfile interop fixture (opt-in). It runs the pinned Node contender
// that reproduces Claude Code's two-lock refresh sequence against Headroom's
// mkdir lock, in both directions.
//
// Run it locally from server/:
//
//	(cd internal/providers/claude/testdata/lockfile-interop && npm ci --ignore-scripts)
//	HEADROOM_PROPER_LOCKFILE_FIXTURE=1 go test -run LockfileInterop ./internal/providers/claude
//
// CI runs it on Linux after `npm ci --ignore-scripts`. proper-lockfile clamps
// stale to 2000ms and update to 1000ms, so the fixture uses those values; waits
// past them are the staleness behavior under test.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireProperLockfileFixture(t *testing.T) []string {
	t.Helper()
	if os.Getenv(properLockfileFixtureEnv) != "1" {
		t.Skip("set " + properLockfileFixtureEnv + "=1 to run the proper-lockfile interop fixture")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required for the proper-lockfile interop fixture: %v", err)
	}
	script := contenderPath(t)
	if _, err := os.Stat(filepath.Join(filepath.Dir(script), "node_modules", "proper-lockfile")); err != nil {
		t.Fatalf("install the pinned contender with `npm ci --ignore-scripts` in %s: %v", filepath.Dir(script), err)
	}
	return []string{node, script}
}

func interopLockConfig(stale, heartbeat time.Duration) claudeCodeLockConfig {
	cfg := testLockConfig()
	cfg.maxAttempts = 1
	cfg.staleAge = stale
	cfg.heartbeatInterval = heartbeat
	return cfg
}

func Test_LockfileInterop_mutual_exclusion_both_ways(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	dir := t.TempDir()

	holder := startContender(t, argv, "hold", dir, "2000", "1000")
	holder.expect("LOCKED")
	if _, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(2*time.Second, 0)); !errors.Is(err, errClaudeCodeRefreshing) {
		t.Fatalf("Headroom acquired a lock Node holds: %v", err)
	}
	holder.release()

	lock, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(2*time.Second, 0))
	if err != nil {
		t.Fatalf("Headroom acquire: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()
	for _, name := range []string{"new", "legacy"} {
		if got := runContender(t, argv, "try", dir, "2000", "1000", name); got != "ELOCKED:"+name {
			t.Fatalf("Node %s lock = %q, want ELOCKED:%s", name, got, name)
		}
	}
	if got := runContender(t, argv, "check", dir, "2000", "new"); got != "FRESH" {
		t.Fatalf("Node check = %q, want FRESH", got)
	}
}

func Test_LockfileInterop_heartbeat_keeps_node_out(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	dir := t.TempDir()

	lock, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(2*time.Second, 250*time.Millisecond))
	if err != nil {
		t.Fatalf("Headroom acquire: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	// Past Node's 2s staleness floor, only Headroom's heartbeat keeps the lock
	// fresh; without it Node would reclaim it.
	time.Sleep(2500 * time.Millisecond)
	if got := runContender(t, argv, "check", dir, "2000", "new"); got != "FRESH" {
		t.Fatalf("Node check after the stale window = %q, want FRESH", got)
	}
	if got := runContender(t, argv, "try", dir, "2000", "1000", "new"); got != "ELOCKED:new" {
		t.Fatalf("Node reclaimed a heartbeating lock: %q", got)
	}
}

func Test_LockfileInterop_headroom_reclaims_stale_node_lock(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	dir := t.TempDir()

	holder := startContender(t, argv, "hold", dir, "2000", "1000")
	holder.expect("LOCKED")
	holder.kill()
	waitForStale(t, filepath.Join(dir, claudeCodeLockName), 2*time.Second)

	lock, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(2*time.Second, 0))
	if err != nil {
		t.Fatalf("Headroom did not reclaim a stale Node lock: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func Test_LockfileInterop_node_reclaims_stale_headroom_lock(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	dir := t.TempDir()

	lock, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(2*time.Second, 0))
	if err != nil {
		t.Fatalf("Headroom acquire: %v", err)
	}
	waitForStale(t, filepath.Join(dir, claudeCodeLockName), 2*time.Second)
	if got := runContender(t, argv, "try", dir, "2000", "1000", "new"); got != "ACQUIRED" {
		t.Fatalf("Node did not reclaim a stale Headroom lock: %q", got)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func Test_LockfileInterop_probed_mtime_is_not_stale(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	dir := t.TempDir()

	holder := startContender(t, argv, "hold", dir, "2000", "1000")
	holder.expect("LOCKED")
	// proper-lockfile's first acquisition probes mtime precision by setting the
	// lock directory's mtime up to a second in the future. Headroom must read
	// that as fresh even with a staleness window shorter than the probe offset.
	if _, err := acquireClaudeCodeLock(context.Background(), dir, interopLockConfig(100*time.Millisecond, 0)); !errors.Is(err, errClaudeCodeRefreshing) {
		t.Fatalf("Headroom treated a probe-dated Node lock as stale: %v", err)
	}
	holder.release()
}

func Test_LockfileInterop_legacy_lock_through_symlinked_config_dir(t *testing.T) {
	argv := requireProperLockfileFixture(t)
	root := t.TempDir()
	realDir := filepath.Join(root, "real-config")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatalf("create real config dir: %v", err)
	}
	if err := os.Symlink(realDir, configDir); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	lock, err := acquireClaudeCodeLock(context.Background(), configDir, interopLockConfig(2*time.Second, 0))
	if err != nil {
		t.Fatalf("Headroom acquire through a symlinked config dir: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}()

	// Claude Code resolves the config directory before taking the legacy lock,
	// so Headroom must place it beside the target, not beside the symlink.
	if _, err := os.Lstat(realDir + ".lock"); err != nil {
		t.Fatalf("legacy lock missing beside the resolved directory: %v", err)
	}
	if got := runContender(t, argv, "try", configDir, "2000", "1000", "legacy"); got != "ELOCKED:legacy" {
		t.Fatalf("Node legacy lock = %q, want ELOCKED:legacy", got)
	}
}
