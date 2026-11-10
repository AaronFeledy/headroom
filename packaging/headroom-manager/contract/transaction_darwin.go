//go:build darwin

package contract

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type darwinWatch struct {
	pid               int
	token, executable string
}

var errDarwinExecutableUnavailable = errors.New("process executable identity is unavailable")

// captureProcessToken combines the kernel-recorded executable path with the
// process start timeval. A reused PID therefore cannot authorize an update or
// be stopped as the original Headroom process.
func captureProcessToken(pid int, expected string) (string, error) {
	before, err := darwinStartToken(pid)
	if err != nil {
		return "", err
	}
	actual, err := darwinProcessPath(pid)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errDarwinExecutableUnavailable, err)
	}
	if !samePath(actual, expected) {
		return "", errProcessExecutableMismatch
	}
	after, err := darwinStartToken(pid)
	if err != nil || before != after {
		return "", errors.New("process creation identity changed")
	}
	return after, nil
}

func darwinStartToken(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec <= 0 {
		return "", errors.New("process creation identity is invalid")
	}
	return fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}

func darwinProcessPath(pid int) (string, error) {
	// proc_pidpath is a libproc wrapper around the proc_info syscall. Calling
	// PROC_PIDPATHINFO directly keeps Darwin builds cgo-free while obtaining the
	// kernel's executable vnode path rather than mutable argv data.
	const (
		procInfoCallPIDInfo = 2
		procPIDPathInfo     = 11
		procPIDPathMax      = 4096
	)
	buffer := make([]byte, procPIDPathMax)
	_, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDPathInfo, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if errno != 0 {
		return "", errno
	}
	// Like libproc's proc_pidpath(), read the NUL-terminated buffer. The kernel
	// does not populate the syscall return count for PROC_PIDPATHINFO.
	end := bytes.IndexByte(buffer, 0)
	if end <= 0 {
		return "", errors.New("process executable path is empty or unterminated")
	}
	return string(buffer[:end]), nil
}

func watchProcess(pid int, expected, token string) (processWatch, error) {
	got, err := captureProcessToken(pid, expected)
	if err != nil || got != token {
		return nil, errors.New("process creation identity changed")
	}
	return &darwinWatch{pid: pid, token: token, executable: expected}, nil
}

func (w *darwinWatch) Wait(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if processGone(w.pid) {
			return nil
		}
		got, err := captureProcessToken(w.pid, w.executable)
		if err != nil {
			// Kernel identity can disappear after the preceding liveness check.
			// PROC_PIDPATHINFO may become unavailable just before kern.proc marks an
			// exiting process as a zombie. Retry that transition only while the
			// immutable creation identity still matches. A different executable
			// path remains an immediate hard failure.
			if processGone(w.pid) {
				return nil
			}
			if errors.Is(err, errDarwinExecutableUnavailable) {
				start, startErr := darwinStartToken(w.pid)
				if startErr == nil && start == w.token {
					time.Sleep(25 * time.Millisecond)
					continue
				}
				if processGone(w.pid) {
					return nil
				}
			}
			return fmt.Errorf("cannot verify watched process: %w", err)
		}
		if got != w.token {
			return errors.New("process identity changed while waiting")
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("process exit timed out")
}

func (w *darwinWatch) KillWait(timeout time.Duration) error {
	got, err := captureProcessToken(w.pid, w.executable)
	if processGone(w.pid) {
		return nil
	}
	if err != nil || got != w.token {
		return errors.New("refusing to stop a process with changed identity")
	}
	if err = syscall.Kill(w.pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	if err = w.Wait(timeout); err == nil {
		return nil
	}
	if processGone(w.pid) {
		return nil
	}
	got, err = captureProcessToken(w.pid, w.executable)
	if err != nil || got != w.token {
		return errors.New("cannot verify process before forced stop")
	}
	if err = syscall.Kill(w.pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return w.Wait(timeout)
}

func (w *darwinWatch) Close() error { return nil }

type darwinLock struct{ file *os.File }

func acquireInstallLock(root string, timeout time.Duration) (installLock, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(root, "transaction.lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || pathIsLinkOrReparse(lockPath, info)) {
		return nil, errors.New("install transaction lock is unsafe")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened, openErr := file.Stat()
	linked, linkErr := os.Lstat(lockPath)
	if openErr != nil || linkErr != nil || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		file.Close()
		return nil, errors.New("install transaction lock changed while opening")
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &darwinLock{file}, nil
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, errors.New("Headroom install transaction is busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (l *darwinLock) Close() error {
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func replaceAtomic(source, destination string) error { return os.Rename(source, destination) }
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func runtimeWindows() bool { return false }

// processCreationChanged proves a PID now names a different process even when
// its executable path is unavailable, because kern.proc.pid is not
// owner-restricted. An unreadable or matching identity proves nothing.
func processCreationChanged(pid int, token string) bool {
	current, err := darwinStartToken(pid)
	return err == nil && current != token
}

func processGone(pid int) bool {
	// kern.proc.pid reports a missing, fully reaped process as an empty sysctl
	// result, which x/sys surfaces as EIO. Confirm absence through kill(2)
	// instead of treating that otherwise ambiguous sysctl error as process exit.
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	return int(info.Proc.P_pid) != pid || info.Proc.P_stat == 5 // SZOMB
}
