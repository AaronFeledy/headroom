package credstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// UpdateFile locks the resolved target, edits its current bytes, and replaces it atomically.
// A missing file supplies nil to mutator and uses newMode; existing permissions are kept.
// Newly created parent directories use 0700. Symlinks are retained, never replaced.
// Context is checked before commit. UpdateFile does not apply Refresh writability
// preflight, no-op skip, or post-prepare cancellation immunity.
func UpdateFile(ctx context.Context, path string, newMode os.FileMode, mutator Mutator) (err error) {
	if mutator == nil {
		return ErrNilMutator
	}
	target, err := resolveFileTarget(path, 0)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create file directory: %w", err)
	}

	lock, err := acquireFileLock(ctx, target+".lock")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()

	if err := ensureSameTarget(path, target, "while locking"); err != nil {
		return err
	}

	snapshot, err := LoadSnapshot(ctx, target)
	if errors.Is(err, os.ErrNotExist) {
		snapshot = Snapshot{Mode: newMode}
	} else if err != nil {
		return err
	}
	current := snapshot.Data
	if current != nil {
		current = append([]byte(nil), current...)
	}

	updated, err := mutator(current)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("commit credentials canceled: %w", ctx.Err())
	default:
	}

	if err := ensureSameTarget(path, target, "before commit"); err != nil {
		return err
	}

	tmp, err := createTempFile(filepath.Dir(target), filepath.Base(target))
	if err != nil {
		return err
	}
	return commitPreparedTemp(tmp, target, updated, snapshot.Mode, func() error {
		return ensureSameTarget(path, target, "before rename")
	})
}
