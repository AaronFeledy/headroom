//go:build linux

package contract

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinuxWatchWaitsForVerifiedProcessExit(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	process := startFixturePath(t, fixtureExecutable)
	defer func() {
		_ = process.Process.Kill()
		_, _ = process.Process.Wait()
	}()
	token, err := captureProcessToken(process.Process.Pid, fixtureExecutable)
	if err != nil {
		t.Fatal(err)
	}
	start, err := linuxStartToken(process.Process.Pid)
	if err != nil || start != token {
		t.Fatalf("independent start identity = %q, %v; want %q", start, err, token)
	}
	watch, err := watchProcess(process.Process.Pid, fixtureExecutable, token)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err = process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = watch.Wait(3 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxWatchRejectsChangedLiveIdentity(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	process := startFixturePath(t, fixtureExecutable)
	defer func() {
		_ = process.Process.Kill()
		_, _ = process.Process.Wait()
	}()
	token, err := captureProcessToken(process.Process.Pid, fixtureExecutable)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, executable, token string }{
		{"wrong executable", filepath.Join(t.TempDir(), "wrong"), token},
		{"wrong creation identity", fixtureExecutable, token + "-wrong"},
	} {
		t.Run(test.name, func(t *testing.T) {
			watch := &unixWatch{pid: process.Process.Pid, executable: test.executable, token: test.token}
			if err := watch.Wait(time.Second); err == nil {
				t.Fatal("changed live identity accepted while waiting")
			}
			if err := watch.KillWait(time.Second); err == nil {
				t.Fatal("changed live identity accepted before signal")
			}
			got, err := captureProcessToken(process.Process.Pid, fixtureExecutable)
			if err != nil || got != token {
				t.Fatalf("identity rejection disturbed process: %q, %v", got, err)
			}
		})
	}
}

func TestLinuxWatchLiveProcessWaitRemainsBounded(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	token, err := captureProcessToken(os.Getpid(), executable)
	if err != nil {
		t.Fatal(err)
	}
	watch := &unixWatch{pid: os.Getpid(), executable: executable, token: token}
	if err := watch.Wait(30 * time.Millisecond); err == nil {
		t.Fatal("live process reported exited")
	}
}

// After a reboot a stale service receipt's PID can name another user's process
// whose executable link is unreadable. Only its creation identity proves reuse.
func TestLinuxServiceRegistrationReplacesUnreadableReusedPID(t *testing.T) {
	const pid = 1
	if _, err := os.Readlink("/proc/1/exe"); err == nil {
		t.Skip("PID 1 executable link is readable; this needs another user's process")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureProcessToken(pid, executable); err == nil || errors.Is(err, errProcessExecutableMismatch) {
		t.Fatalf("unreadable executable must not be a definite identity: %v", err)
	}
	start, err := linuxStartToken(pid)
	if err != nil {
		t.Fatal(err)
	}
	record := ManagedServiceRecord{PID: pid, Executable: executable, ProcessToken: start}
	if err := managedServiceMayRegister(record); err == nil {
		t.Fatal("unverifiable receipt with a matching creation identity was replaced")
	}
	record.ProcessToken = start + "-previous-boot"
	if err := managedServiceMayRegister(record); err != nil {
		t.Fatalf("reused PID with a different creation identity refused: %v", err)
	}
}
