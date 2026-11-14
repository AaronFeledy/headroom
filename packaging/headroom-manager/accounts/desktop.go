package accounts

import (
	"context"
	"errors"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

// DesktopRestart reports what a running Headroom desktop did when asked to
// restart the local server it started.
type DesktopRestart uint8

const (
	// DesktopUnavailable means no desktop is running, or it is too old to
	// accept the request; nothing was restarted.
	DesktopUnavailable DesktopRestart = iota + 1
	// DesktopNotOwner means the desktop did not start the server it uses
	// (it attached to one, or uses SSH or HTTP(S)); nothing was restarted.
	DesktopNotOwner
	// DesktopOtherConfig means the desktop's server reads another config file;
	// nothing was restarted.
	DesktopOtherConfig
	// DesktopBusy means an update is in progress; nothing was restarted.
	DesktopBusy
	// DesktopRestarted means the desktop restarted its server and the new one
	// is available.
	DesktopRestarted
)

// ErrDesktopOutcomeUnknown means the request was interrupted before the desktop
// replied, so its server may or may not have restarted with the change.
var ErrDesktopOutcomeUnknown = errors.New("the Headroom desktop did not report whether its server restarted")

// Desktop asks a running Headroom desktop to restart the local server it
// started, only when that server reads configPath. An error wrapping
// ErrDesktopOutcomeUnknown leaves the outcome open; any other error means a
// restart was attempted and the new server did not become available.
type Desktop interface {
	RestartServer(ctx context.Context, configPath string) (DesktopRestart, error)
}

func (o Options) restartDesktop(ctx context.Context, target accountconfig.Target, change accountconfig.Change) (restartOutcomeKind, error) {
	if o.Desktop == nil {
		return manualRestart, nil
	}
	result, err := o.Desktop.RestartServer(ctx, change.Path)
	if errors.Is(err, ErrDesktopOutcomeUnknown) {
		// The server may already run with the change; undoing it now could
		// leave the file and the running server disagreeing.
		return 0, &restartError{kind: notRestarted, Path: change.Path, desktop: true, cause: err}
	}
	if err != nil {
		recoveryCtx := context.WithoutCancel(ctx)
		target.Path = change.Path
		if restoreErr := accountconfig.Restore(recoveryCtx, target, change.Before, change.After); restoreErr != nil {
			return 0, &restartError{kind: notUndone, Path: change.Path, desktop: true, cause: errors.Join(err, restoreErr)}
		}
		result, restartErr := o.Desktop.RestartServer(recoveryCtx, change.Path)
		if restartErr != nil || result != DesktopRestarted {
			return 0, &restartError{kind: undoneNotRestarted, Path: change.Path, desktop: true, cause: errors.Join(err, restartErr)}
		}
		return 0, &restartError{kind: rolledBack, Path: change.Path, desktop: true, cause: err}
	}
	switch result {
	case DesktopRestarted:
		return desktopRestarted, nil
	case DesktopOtherConfig:
		return desktopOtherConfig, nil
	case DesktopBusy:
		return desktopBusy, nil
	default:
		return manualRestart, nil
	}
}
