//go:build linux

package contract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type unixWatch struct {
	pid               int
	token, executable string
}

func captureProcessToken(pid int, expected string) (string, error) {
	actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	if !samePath(actual, expected) {
		return "", errProcessExecutableMismatch
	}
	return linuxStartToken(pid)
}

// linuxStartToken remains available while an exiting process drops its
// executable link, before /proc reports the zombie state.
func linuxStartToken(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return "", errors.New("process creation identity is invalid")
	}
	fields := strings.Fields(string(data)[end+2:])
	if len(fields) < 20 {
		return "", errors.New("process creation identity is incomplete")
	}
	return fields[19], nil
}

func watchProcess(pid int, expected, token string) (processWatch, error) {
	got, err := captureProcessToken(pid, expected)
	if err != nil || got != token {
		return nil, errors.New("process creation identity changed")
	}
	return &unixWatch{pid: pid, token: token, executable: expected}, nil
}

func (w *unixWatch) Wait(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if processGone(w.pid) {
			return nil
		}
		got, err := captureProcessToken(w.pid, w.executable)
		if err != nil {
			// /proc can remove the executable link before reporting the zombie
			// state. Wait out that transition only while the immutable creation
			// identity still matches; a different executable remains an error.
			if processGone(w.pid) {
				return nil
			}
			if errors.Is(err, os.ErrNotExist) {
				start, startErr := linuxStartToken(w.pid)
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
func (w *unixWatch) KillWait(timeout time.Duration) error {
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
	got, verifyErr := captureProcessToken(w.pid, w.executable)
	if verifyErr != nil || got != w.token {
		return errors.New("cannot verify process before forced stop")
	}
	if err = syscall.Kill(w.pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return w.Wait(timeout)
}
func (w *unixWatch) Close() error { return nil }

type unixLock struct{ file *os.File }

func acquireInstallLock(root string, timeout time.Duration) (installLock, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(root, "transaction.lock")
	if info, statErr := os.Lstat(lockPath); statErr == nil && (!info.Mode().IsRegular() || pathIsLinkOrReparse(lockPath, info)) {
		return nil, errors.New("install transaction lock is unsafe")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
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
			return &unixLock{file}, nil
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, errors.New("Headroom install transaction is busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (l *unixLock) Close() error {
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
func processGone(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(data), ") ")
	return end >= 0 && len(data) > end+2 && data[end+2] == 'Z'
}
