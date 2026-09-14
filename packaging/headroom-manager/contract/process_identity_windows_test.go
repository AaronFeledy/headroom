//go:build windows

package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsProcessIdentityAcceptsFileAliases(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	executable := filepath.Join(root, "original.exe")
	copyFixture(t, fixtureExecutable, executable)
	alias := filepath.Join(root, "alias.exe")
	if err := os.Link(executable, alias); err != nil {
		t.Fatal(err)
	}
	process := startFixturePath(t, executable)
	defer func() { process.Process.Kill(); process.Wait() }()
	token, err := captureProcessToken(process.Process.Pid, executable)
	if err != nil {
		t.Fatal(err)
	}
	got, err := captureProcessToken(process.Process.Pid, alias)
	if err != nil || got != token {
		t.Fatalf("same-file alias rejected: %q, %v", got, err)
	}
	watch, err := watchProcess(process.Process.Pid, alias, token)
	if err != nil {
		t.Fatal(err)
	}
	watch.Close()
	if _, err := watchProcess(process.Process.Pid, alias, token+"changed"); err == nil {
		t.Fatal("alias bypassed process creation identity")
	}
	ctx, stop, err := LauncherLifetimeContext(context.Background(), process.Process.Pid, alias, token)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if ctx.Err() != context.Canceled || processGone(process.Process.Pid) {
		t.Fatal("watch cleanup must cancel without terminating the process")
	}
	if _, _, err := LauncherLifetimeContext(context.Background(), process.Process.Pid, alias, token+"changed"); err == nil {
		t.Fatal("launcher alias bypassed process creation identity")
	}

	// Identical contents are insufficient: a different file is not the image.
	copyPath := filepath.Join(root, "copy.exe")
	copyFixture(t, executable, copyPath)
	for _, other := range []string{copyPath, filepath.Join(root, "missing.exe"), root} {
		if _, err := captureProcessToken(process.Process.Pid, other); !errors.Is(err, errProcessExecutableMismatch) {
			t.Fatalf("different file accepted: %s: %v", other, err)
		}
		if _, err := watchProcess(process.Process.Pid, other, token); err == nil {
			t.Fatal("watch accepted a different file")
		}
		if _, _, err := LauncherLifetimeContext(context.Background(), process.Process.Pid, other, token); err == nil {
			t.Fatal("launcher watch accepted a different file")
		}
	}
}
