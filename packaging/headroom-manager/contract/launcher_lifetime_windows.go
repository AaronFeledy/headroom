//go:build windows

package contract

import (
	"context"
	"errors"
)

// LauncherLifetimeContext cancels when the exact launcher exits. It holds a
// query/synchronize handle, so PID reuse cannot redirect the watch. It neither
// terminates the launcher nor owns the CLI's detached update helpers.
// The caller must first validate the launcher against its installation.
func LauncherLifetimeContext(ctx context.Context, pid int, executable, token string) (context.Context, context.CancelFunc, error) {
	handle, actual, got, err := openWindowsProcess(pid, processSynchronize|processQueryLimitedInformation)
	if err != nil {
		return nil, nil, err
	}
	if token == "" || got != token || !sameWindowsProcessExecutable(actual, executable) {
		procCloseHandle.Call(uintptr(handle))
		return nil, nil, errors.New("public launcher process identity changed")
	}
	monitored, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer procCloseHandle.Call(uintptr(handle))
		for {
			select {
			case <-monitored.Done():
				return
			default:
			}
			result, _, _ := procWaitForSingleObject.Call(uintptr(handle), 100)
			if result != 0x102 {
				cancel()
				return
			}
		}
	}()
	return monitored, func() { cancel(); <-done }, nil
}
