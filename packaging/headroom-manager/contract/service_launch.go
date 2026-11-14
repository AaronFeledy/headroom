package contract

import (
	"context"
	"errors"
	"slices"
	"time"
)

const defaultManagedServiceReadyTimeoutMS = 30000

// ErrRestartRolledBack reports that a restarted service did not become ready and
// was started again after the caller's rollback.
var ErrRestartRolledBack = errors.New("the service did not start with the change and was restarted after rollback")

type supervisedServiceLaunch struct {
	record   ManagedServiceRecord
	timeout  time.Duration
	onLaunch func(int, string, string) error
}

func launchSupervisedManagedService(ctx context.Context, launch supervisedServiceLaunch) (ManagedServiceRecord, error) {
	expected := launch.record
	_ = clearSupervisedManagedServiceRestart(expected.InstallRoot)
	if err := clearManagedServiceReceipt(expected.InstallRoot); err != nil {
		return ManagedServiceRecord{}, err
	}
	if err := authorizeSupervisedManagedServiceRestart(expected.InstallRoot, expected.Executable, expected.Arguments,
		expected.Environment, expected.WorkingDirectory); err != nil {
		return ManagedServiceRecord{}, err
	}
	failed := func(cause error) (ManagedServiceRecord, error) {
		return ManagedServiceRecord{}, errors.Join(cause, stopManagedServiceSupervisor(expected.Supervisor), clearSupervisedManagedServiceRestart(expected.InstallRoot))
	}
	if err := startManagedServiceSupervisor(expected.Supervisor); err != nil {
		return failed(err)
	}
	deadline := time.Now().Add(launch.timeout)
	journaled := false
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		record, readErr := readManagedService(expected.InstallRoot)
		if readErr == nil && record.Supervisor == expected.Supervisor && samePath(record.Executable, expected.Executable) &&
			slices.Equal(record.Arguments, expected.Arguments) && slices.Equal(record.Environment, expected.Environment) &&
			record.WorkingDirectory == expected.WorkingDirectory {
			if !journaled && launch.onLaunch != nil {
				if err := launch.onLaunch(record.PID, record.Executable, record.ProcessToken); err != nil {
					return failed(err)
				}
				journaled = true
			}
			if record.Ready {
				return record, nil
			}
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return failed(ctx.Err())
		case <-timer.C:
		}
	}
	return failed(errors.New("managed per-user headroom.service did not report the active generation"))
}
