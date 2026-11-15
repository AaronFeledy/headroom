//go:build windows

package credstore

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	movefileReplaceExisting = 0x00000001
	movefileWriteThrough    = 0x00000008

	fileRenameFlagReplaceIfExists = 0x00000001
	fileRenameFlagPOSIXSemantics  = 0x00000002
)

var procMoveFileExW = kernel32.NewProc("MoveFileExW")

// fileRenameInfoEx mirrors FILE_RENAME_INFO as used with FileRenameInfoEx.
type fileRenameInfoEx struct {
	Flags          uint32
	_              uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func replaceExisting(tmpName string, path string) error {
	tmpPtr, err := syscall.UTF16PtrFromString(tmpName)
	if err != nil {
		return fmt.Errorf("encode temp path: %w", err)
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode destination path: %w", err)
	}
	err = moveFileExW(tmpPtr, pathPtr, movefileReplaceExisting|movefileWriteThrough)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return fmt.Errorf("movefileex replace existing: %w", err)
	}
	// A read-only destination keeps the original refusal; only an open writable
	// destination is retried with a POSIX-semantics rename.
	info, statErr := os.Stat(path)
	if statErr != nil || info.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("movefileex replace existing: %w", err)
	}
	// MoveFileEx refuses to replace a destination that is open, even when the
	// reader shares delete access. Headroom's own snapshot readers do, so fall
	// back to a POSIX-semantics rename: it replaces the open file atomically and
	// leaves existing readers on the previous bytes.
	if err := renameOverOpenFile(tmpName, path); err != nil {
		return fmt.Errorf("rename prepared file over open destination: %w", err)
	}
	return nil
}

func renameOverOpenFile(tmpName, path string) (err error) {
	tmpPtr, err := windows.UTF16PtrFromString(tmpName)
	if err != nil {
		return fmt.Errorf("encode temp path: %w", err)
	}
	handle, err := windows.CreateFile(
		tmpPtr,
		windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return fmt.Errorf("open prepared file: %w", err)
	}
	defer func() {
		err = errorsJoin(err, windows.CloseHandle(handle))
	}()
	name, err := windows.UTF16FromString(path)
	if err != nil {
		return fmt.Errorf("encode destination path: %w", err)
	}
	offset := int(unsafe.Offsetof(fileRenameInfoEx{}.FileName))
	buffer := make([]byte, offset+len(name)*2)
	// SAFETY: buffer is offset+len(name)*2 bytes and begins with the layout of
	// fileRenameInfoEx; the name is written into its trailing array, which
	// Windows only reads during this synchronous call.
	info := (*fileRenameInfoEx)(unsafe.Pointer(&buffer[0]))
	info.Flags = fileRenameFlagReplaceIfExists | fileRenameFlagPOSIXSemantics
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(name)), name)
	if err := windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer))); err != nil {
		return fmt.Errorf("rename file: %w", err)
	}
	return nil
}

func syncDirectory(_ string) error { return nil }

func moveFileExW(existingName *uint16, newName *uint16, flags uint32) error {
	// SAFETY: existingName and newName point to Go-owned UTF-16 buffers created by
	// replaceExisting and kept live for this synchronous MoveFileExW call. Windows
	// does not retain either pointer after the call returns.
	ret, _, err := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(existingName)),
		uintptr(unsafe.Pointer(newName)),
		uintptr(flags),
	)
	if ret == 0 {
		return err
	}
	return nil
}
