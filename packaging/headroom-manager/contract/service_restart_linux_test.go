//go:build linux

package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type supervisedServiceFixture struct {
	root     string
	launcher string
	record   ManagedServiceRecord
	state    InstallState
	calls    []string
	onStart  func() error
}

func newSupervisedServiceFixture(t *testing.T) *supervisedServiceFixture {
	t.Helper()
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	parent := t.TempDir()
	f := &supervisedServiceFixture{root: filepath.Join(parent, "install"), launcher: filepath.Join(parent, "bin", "headroom")}
	archive := makePackage(t, parent, "19.1.0", false)
	state, err := InstallArchiveWithCLIEntry(archive, f.root, filepath.Join(parent, "bin", "headroom-gui"), f.launcher, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	f.state = state
	executable, _, err := ActiveExecutableForRole(f.root, RoleCLI)
	if err != nil {
		t.Fatal(err)
	}
	service := startFixturePath(t, executable)
	token, err := captureProcessToken(service.Process.Pid, executable)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f.record = ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: f.root,
		PID: service.Process.Pid, Executable: executable, ProcessToken: token, Owner: owner,
		Supervisor: managedSystemdUser, Arguments: []string{"--config", "fixture.yaml"},
		Environment: []string{"USAGE_PROVIDER_CODEX_ENABLED=false"}, WorkingDirectory: parent, Ready: true}
	if err := writeManagedService(f.root, f.record); err != nil {
		t.Fatal(err)
	}
	previous := userSystemctlCommand
	t.Cleanup(func() { userSystemctlCommand = previous })
	userSystemctlCommand = func(arguments ...string) ([]byte, error) {
		if arguments[len(arguments)-1] != managedUserUnit {
			t.Fatalf("unexpected unit: %v", arguments)
		}
		switch arguments[0] {
		case "show":
			return []byte(strconv.Itoa(f.record.PID)), nil
		case "stop":
			f.calls = append(f.calls, "stop")
		case "start":
			f.calls = append(f.calls, "start")
			if f.onStart != nil {
				return nil, f.onStart()
			}
		default:
			t.Fatalf("unexpected systemctl command: %v", arguments)
		}
		return nil, nil
	}
	return f
}

func TestRestartSupervisedManagedServiceReadyWithNewIdentity(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	previous := f.record
	f.onStart = func() error { return f.startReplacement(t) }

	record, err := RestartSupervisedManagedService(context.Background(), f.root, previous, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !record.Ready || (record.PID == previous.PID && record.ProcessToken == previous.ProcessToken) || record.ConfigPath != f.record.ConfigPath ||
		!reflect.DeepEqual(record.Arguments, previous.Arguments) || !reflect.DeepEqual(record.Environment, previous.Environment) || record.WorkingDirectory != previous.WorkingDirectory {
		t.Fatalf("replacement receipt = %+v", record)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
}

// startReplacement plays the systemd-started serve: it must find the install
// lock held, pass the launcher handoff, register, and report readiness.
func (f *supervisedServiceFixture) startReplacement(t *testing.T) error {
	if lock, err := acquireInstallLock(f.root, 0); err == nil {
		lock.Close()
		t.Fatal("restart released install lock before service start")
	}
	if err := AuthorizeSupervisedLauncherHandoff(f.root, f.launcher, f.record.PID, append([]string{"serve"}, f.record.Arguments...)); err != nil {
		return err
	}
	service := startFixturePath(t, f.record.Executable)
	f.record.PID = service.Process.Pid
	token, err := captureProcessToken(f.record.PID, f.record.Executable)
	if err != nil {
		return err
	}
	f.record.ProcessToken, f.record.Ready = token, false
	if err := writeManagedService(f.root, f.record); err != nil {
		return err
	}
	f.record.ConfigPath = filepath.Join(f.record.WorkingDirectory, "resolved.yaml")
	return MarkManagedServiceReady(f.record)
}

func TestRestartSupervisedManagedServiceRollsBackThenStartsAgain(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	previous := f.record
	starts := 0
	f.onStart = func() error {
		starts++
		if starts == 1 {
			return errors.New("unit failed to start")
		}
		return f.startReplacement(t)
	}
	rolledBack := false

	record, err := RestartSupervisedManagedService(context.Background(), f.root, previous, func() error {
		if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
			t.Fatalf("rollback ran before the failed unit stopped: %v", f.calls)
		}
		rolledBack = true
		return nil
	})

	if !errors.Is(err, ErrRestartRolledBack) || !rolledBack {
		t.Fatalf("restart error = %v, rolled back = %v", err, rolledBack)
	}
	if !record.Ready || record.PID == previous.PID {
		t.Fatalf("replacement receipt = %+v", record)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop", "start"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
}

func TestRestartSupervisedManagedServiceFailedRollbackLeavesUnitStopped(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	f.onStart = func() error { return errors.New("unit failed to start") }
	rollbackErr := errors.New("rollback failed")

	_, err := RestartSupervisedManagedService(context.Background(), f.root, f.record, func() error { return rollbackErr })

	if !errors.Is(err, rollbackErr) || errors.Is(err, ErrRestartRolledBack) {
		t.Fatalf("restart error = %v", err)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
}

func TestRestartSupervisedManagedServiceRefusesChangedReceiptWithoutSystemctl(t *testing.T) {
	for _, change := range []string{"token", "supervisor"} {
		t.Run(change, func(t *testing.T) {
			f := newSupervisedServiceFixture(t)
			previous := f.record
			if change == "token" {
				previous.ProcessToken = "different"
			} else {
				f.record.Supervisor = ""
				if err := writeManagedService(f.root, f.record); err != nil {
					t.Fatal(err)
				}
			}
			userSystemctlCommand = func(...string) ([]byte, error) {
				t.Fatal("receipt mismatch invoked systemctl")
				return nil, nil
			}

			if _, err := RestartSupervisedManagedService(context.Background(), f.root, previous, nil); err == nil {
				t.Fatal("changed receipt accepted")
			}
		})
	}
}

func TestRestartSupervisedManagedServiceCancellationCleansIntent(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onStart = func() error { cancel(); return nil }

	_, err := RestartSupervisedManagedService(ctx, f.root, f.record, nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restart error = %v", err)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(f.root, "runtime", supervisedRestartRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart intent remains: %v", err)
	}
}

func TestSupervisedManagedServiceReadinessTimeoutStopsWithoutRetry(t *testing.T) {
	f := newSupervisedServiceFixture(t)

	_, err := launchSupervisedManagedService(context.Background(), supervisedServiceLaunch{record: f.record, timeout: time.Millisecond})

	if err == nil {
		t.Fatal("missing ready receipt accepted")
	}
	if !reflect.DeepEqual(f.calls, []string{"start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(f.root, "runtime", supervisedRestartRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart intent remains: %v", err)
	}
}

func TestRestartSupervisedManagedServiceRefusesReplacementReadingAnotherConfig(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	f.record.ConfigPath = filepath.Join(f.record.WorkingDirectory, "edited.yaml")
	if err := writeManagedService(f.root, f.record); err != nil {
		t.Fatal(err)
	}
	previous := f.record
	f.onStart = func() error { return f.startReplacement(t) }

	_, err := RestartSupervisedManagedService(context.Background(), f.root, previous, nil)

	if err == nil {
		t.Fatal("replacement reading another config was accepted")
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
}
