//go:build windows

package contract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var procOpenProcess = kernel32.NewProc("OpenProcess")
var procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
var procGetProcessTimes = kernel32.NewProc("GetProcessTimes")
var procWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
var procCloseHandle = kernel32.NewProc("CloseHandle")
var procTerminateProcess = kernel32.NewProc("TerminateProcess")
var procLockFileEx = kernel32.NewProc("LockFileEx")
var procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
var procMoveFileExW = kernel32.NewProc("MoveFileExW")

const processSynchronize = 0x00100000
const processQueryLimitedInformation = 0x1000
const processTerminate = 0x0001

type windowsWatch struct {
	handle syscall.Handle
	token  string
}

func openWindowsProcess(pid int, access uintptr) (syscall.Handle, string, string, error) {
	h, _, callErr := procOpenProcess.Call(access, 0, uintptr(pid))
	if h == 0 {
		return 0, "", "", callErr
	}
	handle := syscall.Handle(h)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if ok, _, err := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size))); ok == 0 {
		procCloseHandle.Call(h)
		return 0, "", "", err
	}
	var creation, exit, kernel, user syscall.Filetime
	if ok, _, err := procGetProcessTimes.Call(h, uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		procCloseHandle.Call(h)
		return 0, "", "", err
	}
	token := fmt.Sprintf("%08x%08x", creation.HighDateTime, creation.LowDateTime)
	return handle, syscall.UTF16ToString(buffer[:size]), token, nil
}

// sameWindowsProcessExecutable accepts Windows path aliases only when both
// names resolve to the same on-disk file. MSIX app-data virtualization can make
// os.Executable and QueryFullProcessImageNameW report different names for it.
// Keep this separate from package/receipt path validation, which remains strict.
func sameWindowsProcessExecutable(actual, expected string) bool {
	if samePath(actual, expected) {
		return true
	}
	if !filepath.IsAbs(actual) || !filepath.IsAbs(expected) {
		return false
	}
	actualInfo, actualErr := os.Stat(actual)
	expectedInfo, expectedErr := os.Stat(expected)
	return actualErr == nil && expectedErr == nil && actualInfo.Mode().IsRegular() &&
		expectedInfo.Mode().IsRegular() && os.SameFile(actualInfo, expectedInfo)
}

func captureProcessToken(pid int, expected string) (string, error) {
	h, actual, token, err := openWindowsProcess(pid, processSynchronize|processQueryLimitedInformation)
	if err != nil {
		return "", err
	}
	procCloseHandle.Call(uintptr(h))
	if !sameWindowsProcessExecutable(actual, expected) {
		return "", errProcessExecutableMismatch
	}
	return token, nil
}

func watchProcess(pid int, expected, token string) (processWatch, error) {
	h, actual, got, err := openWindowsProcess(pid, processSynchronize|processQueryLimitedInformation|processTerminate)
	if err != nil || !sameWindowsProcessExecutable(actual, expected) || got != token {
		if h != 0 {
			procCloseHandle.Call(uintptr(h))
		}
		return nil, errors.New("process creation identity changed")
	}
	return &windowsWatch{handle: h, token: token}, nil
}
func (w *windowsWatch) Wait(timeout time.Duration) error {
	result, _, err := procWaitForSingleObject.Call(uintptr(w.handle), uintptr(uint32(timeout/time.Millisecond)))
	if result == 0 {
		return nil
	}
	if result == 0x102 {
		return errors.New("process exit timed out")
	}
	return err
}
func (w *windowsWatch) KillWait(timeout time.Duration) error {
	if result, _, err := procWaitForSingleObject.Call(uintptr(w.handle), 0); result == 0 {
		return nil
	} else if result == 0xffffffff {
		return err
	}
	if ok, _, err := procTerminateProcess.Call(uintptr(w.handle), 1); ok == 0 {
		if result, _, _ := procWaitForSingleObject.Call(uintptr(w.handle), 0); result == 0 {
			return nil
		}
		return err
	}
	return w.Wait(timeout)
}
func (w *windowsWatch) Close() error { procCloseHandle.Call(uintptr(w.handle)); return nil }

type windowsLock struct {
	file    *os.File
	overlap syscall.Overlapped
}

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
	lock := &windowsLock{file: file}
	deadline := time.Now().Add(timeout)
	for {
		ok, _, _ := procLockFileEx.Call(file.Fd(), 0x2|0x1, 0, 1, 0, uintptr(unsafe.Pointer(&lock.overlap)))
		if ok != 0 {
			return lock, nil
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, errors.New("Headroom install transaction is busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (l *windowsLock) Close() error {
	procUnlockFileEx.Call(l.file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&l.overlap)))
	return l.file.Close()
}
func runtimeWindows() bool { return true }
func replaceAtomic(source, destination string) error {
	from, _ := syscall.UTF16PtrFromString(source)
	to, _ := syscall.UTF16PtrFromString(destination)
	ok, _, err := procMoveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8)
	if ok == 0 {
		return err
	}
	return nil
}
func syncDirectory(string) error { return nil }

var openSynchronizeProcess = func(pid int) (uintptr, error) {
	h, _, callErr := procOpenProcess.Call(processSynchronize, 0, uintptr(pid))
	return h, callErr
}

func processGone(pid int) bool {
	h, callErr := openSynchronizeProcess(pid)
	if h == 0 {
		return errors.Is(callErr, syscall.Errno(87)) || errors.Is(callErr, syscall.Errno(1168))
	}
	defer procCloseHandle.Call(h)
	result, _, _ := procWaitForSingleObject.Call(h, 0)
	return result == 0
}
