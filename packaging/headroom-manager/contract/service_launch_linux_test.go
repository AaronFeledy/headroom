//go:build linux

package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRestartSupervisedManagedServiceReadyDeadlineStopsWithoutRetry(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := RestartSupervisedManagedService(ctx, f.root, f.record, nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restart error = %v", err)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(f.root, "runtime", supervisedRestartRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart intent remains: %v", err)
	}
}

func TestRestartSupervisedManagedServiceRefusesUnchangedProcess(t *testing.T) {
	f := newSupervisedServiceFixture(t)
	f.onStart = func() error { return writeManagedService(f.root, f.record) }

	_, err := RestartSupervisedManagedService(context.Background(), f.root, f.record, nil)

	if err == nil {
		t.Fatal("unchanged ready process accepted")
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop"}) {
		t.Fatalf("supervisor operations = %v", f.calls)
	}
}

func TestApplyLaunchSupervisedManagedServiceJournalsBeforeReadiness(t *testing.T) {
	for _, failJournal := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "journal_failure"}[failJournal], func(t *testing.T) {
			f := newSupervisedServiceFixture(t)
			f.record.Ready = false
			f.onStart = func() error { return writeManagedService(f.root, f.record) }
			journalError := errors.New("fixture journal failure")
			journalCalls := 0
			// Both cases return as soon as the receipt is ready or the journal fails;
			// the bound only has to outlast the fsynced receipt writes on a slow disk.
			request := ApplyRequest{InstallRoot: f.root, ManagedServiceSupervisor: managedSystemdUser,
				ManagedServiceArguments: f.record.Arguments, ManagedServiceEnvironment: f.record.Environment,
				ManagedServiceWorkingDirectory: f.record.WorkingDirectory, ReadyTimeoutMS: 5000}

			err := launchManagedService(request, f.state, func(pid int, executable, token string) error {
				journalCalls++
				if pid != f.record.PID || executable != f.record.Executable || token != f.record.ProcessToken {
					t.Fatal("journaled a different process")
				}
				stored, err := readManagedService(f.root)
				if err != nil || stored.Ready {
					t.Fatalf("receipt was ready before journaling: %v", err)
				}
				if failJournal {
					return journalError
				}
				f.record.ConfigPath = filepath.Join(f.record.WorkingDirectory, "resolved.yaml")
				return MarkManagedServiceReady(f.record)
			})

			if journalCalls != 1 || (failJournal && !errors.Is(err, journalError)) || (!failJournal && err != nil) {
				t.Fatalf("launch result: journal calls %d, error %v", journalCalls, err)
			}
			want := []string{"start"}
			if failJournal {
				want = append(want, "stop")
			} else if ready, err := ReadManagedService(f.root); err != nil || ready.ConfigPath != f.record.ConfigPath {
				t.Fatalf("ready config path = %q, %v", ready.ConfigPath, err)
			}
			if !reflect.DeepEqual(f.calls, want) {
				t.Fatalf("supervisor operations = %v", f.calls)
			}
			if _, err := os.Stat(filepath.Join(f.root, "runtime", supervisedRestartRecordName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restart intent remains: %v", err)
			}
		})
	}
}
