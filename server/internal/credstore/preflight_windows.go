//go:build windows

package credstore

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var probeDeleteAccess = windows.CreateFile
var closeDeleteProbe = windows.CloseHandle

func isPlatformNotWritable(err error) bool {
	return errors.Is(err, windows.ERROR_WRITE_PROTECT)
}

func preflightReplaceable(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		if isNotWritableFSError(err) {
			return wrapNotWritable(target, err)
		}
		return err
	}
	if info.Mode().Perm()&0o200 == 0 {
		return wrapNotWritable(target, errors.New("read-only attribute"))
	}

	pathPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	handle, err := probeDeleteAccess(
		pathPtr,
		windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return fmt.Errorf("credential file is busy: %w", err)
		}
		if isNotWritableFSError(err) {
			return wrapNotWritable(target, err)
		}
		return err
	}
	if err := closeDeleteProbe(handle); err != nil {
		return fmt.Errorf("close credential replacement probe: %w", err)
	}
	return nil
}
