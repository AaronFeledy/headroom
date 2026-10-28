//go:build windows

package credstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const directoryPendingSuffix = ".headroom-init.pending"

func directoryReady(path string) (bool, error) {
	info, err := os.Stat(path)
	// Check the marker AFTER observing the destination: a concurrent publisher
	// must not install its marker and then make the directory visible between
	// a marker check and this stat. Missing destinations are rechecked locked.
	if _, markerErr := os.Lstat(path + directoryPendingSuffix); !os.IsNotExist(markerErr) {
		return false, fmt.Errorf("directory publication is uncertain")
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("durable directory destination is not a directory")
	}
	return true, nil
}

func ensureDirectory(ctx context.Context, path string, mode os.FileMode) error {
	if ready, err := directoryReady(path); err == nil && ready {
		return nil
	}
	// A pending marker may belong to an initializer still holding the lock.
	// Wait for it, then distinguish completed publication from a failed one.
	return publishDirectory(ctx, path, mode, false)
}

func createDirectory(ctx context.Context, path string, mode os.FileMode) error {
	return publishDirectory(ctx, path, mode, true)
}

func publishDirectory(ctx context.Context, path string, mode os.FileMode, exclusive bool) (err error) {
	// The parent is already durable. These siblings survive an uncertain rename,
	// so another process cannot accept a failed publication just because its
	// destination became visible. Never remove these locks or recover markers.
	lock, err := LockFile(ctx, path+".headroom-init.lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if ready, err := directoryReady(path); err != nil {
		return err
	} else if ready {
		if exclusive {
			return os.ErrExist
		}
		return nil // A different initializer completed under the same lock.
	}
	marker := path + directoryPendingSuffix
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// MoveFileEx WRITE_THROUGH establishes the marker before any directory
	// publication. A failed write keeps the exclusive marker and disables reuse.
	if err := WriteFileAtomically(marker, []byte("1\n"), 0o600); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(path), ".headroom-init-*")
	if err != nil {
		return err
	}
	defer func() {
		// Only our newly created empty temporary directory may be removed. Never
		// recurse into or replace a concurrent destination.
		if removeErr := os.Remove(temporary); !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	source, err := syscall.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	destination, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// Same-volume sibling rename, without REPLACE_EXISTING. Any failure leaves
	// the marker even if the destination exists; existence never proves success.
	if err := moveFileExW(source, destination, movefileWriteThrough); err != nil {
		return err
	}
	// Publication is durable before cleanup. A crash restoring the marker is a
	// safe refusal, so no post-unlink sync is needed.
	return os.Remove(marker)
}
