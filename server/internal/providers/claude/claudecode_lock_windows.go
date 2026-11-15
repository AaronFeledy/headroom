//go:build windows

package claude

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func removeLockDir(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.RemoveDirectory(p)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	return err
}

func isLockPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_WRITE_PROTECT)
}
