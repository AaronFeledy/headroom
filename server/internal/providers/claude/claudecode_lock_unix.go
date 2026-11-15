//go:build !windows

package claude

import (
	"errors"
	"os"
	"syscall"
)

func removeLockDir(path string) error {
	err := syscall.Rmdir(path)
	if err == nil {
		return nil
	}
	if err == syscall.ENOENT {
		return nil
	}
	return err
}

func isLockPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS)
}
