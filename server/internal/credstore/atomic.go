package credstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func AtomicUpdate(ctx context.Context, path string, mutator Mutator) error {
	if mutator == nil {
		return ErrNilMutator
	}
	return lockedReplace(ctx, path, nil, func(current []byte) ([]byte, error) {
		if !json.Valid(current) {
			return nil, fmt.Errorf("current credentials %s: %w", path, ErrInvalidJSON)
		}
		updated, err := mutator(current)
		if err != nil {
			return nil, fmt.Errorf("mutate credentials %s: %w", path, err)
		}
		if !json.Valid(updated) {
			return nil, fmt.Errorf("updated credentials %s: %w", path, ErrInvalidJSON)
		}
		return updated, nil
	})
}

// lockedReplace holds path+".lock", passes a copy of the current bytes to
// mutator, and atomically replaces path. A missing file is an error unless
// missingMode is set, in which case mutator receives nil and the new file uses
// that mode.
func lockedReplace(ctx context.Context, path string, missingMode *os.FileMode, mutator Mutator) (err error) {
	lock, err := acquireFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()

	snapshot, err := LoadSnapshot(ctx, path)
	if errors.Is(err, os.ErrNotExist) && missingMode != nil {
		snapshot = Snapshot{Mode: *missingMode}
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
	return replaceAtomically(path, updated, snapshot.Mode)
}

func replaceAtomically(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp credential file: %w", err)
	}
	tmpName := tmp.Name()
	tmpOpen := true
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()
	defer func() {
		if tmpOpen {
			err = errors.Join(err, tmp.Close())
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temp credential file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp credential file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp credential file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp credential file: %w", err)
	}
	tmpOpen = false
	if err := replaceExisting(tmpName, path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}
