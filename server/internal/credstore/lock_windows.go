//go:build windows

package credstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

type fileLock struct{ file *os.File }

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func acquireFileLock(ctx context.Context, path string) (fileLock, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileLock{}, fmt.Errorf("open credential lock: %w", err)
	}
	handle, err := syscall.CreateFile(
		pathPtr,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fileLock{}, fmt.Errorf("open credential lock: %w", err)
	}
	fileType, err := syscall.GetFileType(handle)
	if err != nil {
		closeErr := syscall.CloseHandle(handle)
		return fileLock{}, fmt.Errorf("inspect credential lock: %w", errorsJoin(err, closeErr))
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		closeErr := syscall.CloseHandle(handle)
		return fileLock{}, fmt.Errorf("inspect credential lock: %w", errorsJoin(err, closeErr))
	}
	if fileType != syscall.FILE_TYPE_DISK ||
		info.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		info.NumberOfLinks != 1 {
		closeErr := syscall.CloseHandle(handle)
		validationErr := errors.New("credential lock must be a regular single-link file")
		return fileLock{}, fmt.Errorf("inspect credential lock: %w", errorsJoin(validationErr, closeErr))
	}
	file := os.NewFile(uintptr(handle), path)
	overlapped := &syscall.Overlapped{}
	for {
		err := lockFileEx(syscall.Handle(file.Fd()), lockfileExclusiveLock|lockfileFailImmediately, overlapped)
		if err == nil {
			if err := writeLockMetadata(file); err != nil {
				unlockErr := unlockFileEx(syscall.Handle(file.Fd()), overlapped)
				closeErr := file.Close()
				return fileLock{}, fmt.Errorf("initialize credential lock: %w", errorsJoin(err, errorsJoin(unlockErr, closeErr)))
			}
			return fileLock{file: file}, nil
		}
		if isLockUnsupported(err) {
			closeErr := file.Close()
			unsupported := fmt.Errorf("%w: %w", errLockUnsupported, err)
			return fileLock{}, fmt.Errorf("lock credentials: %w", errorsJoin(unsupported, closeErr))
		}
		if !isLockContention(err) {
			closeErr := file.Close()
			return fileLock{}, fmt.Errorf("lock credentials: %w", errorsJoin(err, closeErr))
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			closeErr := file.Close()
			return fileLock{}, fmt.Errorf("lock credentials canceled: %w", errorsJoin(ctx.Err(), closeErr))
		case <-timer.C:
		}
	}
}

func (l fileLock) Close() error {
	overlapped := &syscall.Overlapped{}
	if err := unlockFileEx(syscall.Handle(l.file.Fd()), overlapped); err != nil {
		closeErr := l.file.Close()
		return fmt.Errorf("unlock credentials: %w", errorsJoin(err, closeErr))
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close credential lock: %w", err)
	}
	return nil
}

func lockFileEx(handle syscall.Handle, flags uint32, overlapped *syscall.Overlapped) error {
	ret, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}

func isLockContention(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// isLockUnsupported reports a file system that cannot take byte-range locks,
// such as the 9P share behind \\wsl.localhost.
func isLockUnsupported(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_NOT_SUPPORTED)
}

func unlockFileEx(handle syscall.Handle, overlapped *syscall.Overlapped) error {
	ret, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}
