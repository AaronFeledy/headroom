//go:build windows

package credstore

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// openSnapshot opens the credentials file for reading while sharing delete
// access, as libuv does. Without FILE_SHARE_DELETE a reader in Headroom would
// make another writer's replacement fail with a sharing violation.
func openSnapshot(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
