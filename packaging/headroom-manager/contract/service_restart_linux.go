//go:build linux

package contract

import (
	"context"
	"errors"
	"time"
)

// RestartSupervisedManagedService restarts the current user's fixed headroom.service
// under the install lock, checking the previous identity and requiring a ready
// replacement with the same launch context and a new identity. On failure, the
// unit is stopped; a non-nil rollback runs under the lock before one more start.
// A successful second start returns its record with ErrRestartRolledBack and the
// first failure.
func RestartSupervisedManagedService(ctx context.Context, installRoot string, previous ManagedServiceRecord, rollback func() error) (ManagedServiceRecord, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	lock, err := acquireInstallLock(root, 0)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	defer lock.Close()
	// Check the stored identity before ReadManagedService queries systemd.
	current, err := readManagedService(root)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	if current.PID != previous.PID || current.ProcessToken != previous.ProcessToken || current.Supervisor != managedSystemdUser {
		return ManagedServiceRecord{}, errors.New("managed supervised service receipt changed")
	}
	current, err = ReadManagedService(root)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	if err = ctx.Err(); err != nil {
		return ManagedServiceRecord{}, err
	}
	if err = stopManagedServiceSupervisor(current.Supervisor); err != nil {
		return ManagedServiceRecord{}, err
	}
	record, err := relaunchSupervisedManagedService(ctx, root, current)
	if err == nil || rollback == nil {
		return record, err
	}
	// The unit is stopped. Restore the previous state and start it again even
	// if the caller was canceled, so a canceled restart does not leave it down.
	if rollbackErr := rollback(); rollbackErr != nil {
		return ManagedServiceRecord{}, errors.Join(err, rollbackErr)
	}
	record, retryErr := relaunchSupervisedManagedService(context.WithoutCancel(ctx), root, current)
	if retryErr != nil {
		return ManagedServiceRecord{}, errors.Join(err, retryErr)
	}
	return record, errors.Join(ErrRestartRolledBack, err)
}

func relaunchSupervisedManagedService(ctx context.Context, root string, current ManagedServiceRecord) (ManagedServiceRecord, error) {
	record, err := launchSupervisedManagedService(ctx, supervisedServiceLaunch{
		record: current, timeout: time.Duration(defaultManagedServiceReadyTimeoutMS) * time.Millisecond,
		onLaunch: func(pid int, executable, token string) error {
			if pid == current.PID && token == current.ProcessToken {
				return errors.New("managed supervised service process was not replaced")
			}
			return nil
		},
	})
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	ready, err := ReadManagedService(root)
	if err == nil && ready.PID == record.PID && ready.ProcessToken == record.ProcessToken &&
		(ready.PID != current.PID || ready.ProcessToken != current.ProcessToken) {
		if current.ConfigPath == "" || samePath(ready.ConfigPath, current.ConfigPath) {
			return ready, nil
		}
		err = errors.New("managed supervised service started with a different config file")
	}
	return ManagedServiceRecord{}, errors.Join(errors.New("managed supervised service readiness identity changed"), err,
		stopManagedServiceSupervisor(current.Supervisor), clearSupervisedManagedServiceRestart(root))
}
