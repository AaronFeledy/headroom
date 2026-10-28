package credstore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LockFile holds an exclusive cross-process lock until Close. The caller owns
// the transaction, including any reads and durable writes made under the lock.
// The lock file must have a stable path and must never be removed while in use.
func LockFile(ctx context.Context, path string) (io.Closer, error) {
	return acquireFileLock(ctx, path)
}

// WriteFileAtomically syncs the new contents before replacement and syncs the
// containing directory (MoveFileEx WRITE_THROUGH on Windows). It does not lock;
// callers must hold their transaction's LockFile lock.
func WriteFileAtomically(path string, data []byte, mode os.FileMode) error {
	return replaceAtomically(path, data, mode)
}

// SyncDirectory makes a newly created transaction namespace durable. Windows
// replacement durability is provided by WriteFileAtomically's WRITE_THROUGH.
func SyncDirectory(path string) error { return syncDirectory(path) }

// EnsureDirectory durably creates missing ancestors from the filesystem root
// down. Unix also syncs every existing ancestor's parent, so retrying after a
// failed mkdir sync cannot mistake a visible directory for a durable one.
// Windows serializes new directory publication with a persistent failure marker.
func EnsureDirectory(ctx context.Context, path string, mode os.FileMode) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("durable directory path must be absolute")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent == path {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("directory root is not a directory")
		}
		return nil
	}
	if err := EnsureDirectory(ctx, parent, mode); err != nil {
		return err
	}
	return ensureDirectory(ctx, path, mode)
}

// CreateDirectory publishes one new directory exclusively. The caller must
// first make its parent durable with EnsureDirectory. An existing destination
// is always an error; a failed initialization must never replace a namespace.
func CreateDirectory(ctx context.Context, path string, mode os.FileMode) error {
	return createDirectory(ctx, path, mode)
}
