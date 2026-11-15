package credstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Refresh locks path's resolved target, rejects known non-writable stores, and
// passes the target's bytes to prepare. prepare may contact the network. It
// returns the edit to save, or nil to leave the file alone. prepare runs at most
// once; the returned edit runs at most once, does no network I/O, and neither may
// recursively update the same store. ctx is checked while waiting for the lock
// and immediately before prepare; after prepare returns an edit, the re-read and
// commit use context.WithoutCancel. The re-read preserves unrelated changes
// visible at that moment; it is not compare-and-swap against writers that do not
// take Headroom's lock. Known permission or replacement restrictions return
// ErrNotWritable before prepare; failures after the exchange are ordinary errors.
func Refresh(ctx context.Context, path string, prepare func(current []byte) (Mutator, error)) (err error) {
	if prepare == nil {
		return ErrNilMutator
	}
	target, err := resolveFileTarget(path, 0)
	if err != nil {
		return err
	}

	lock, err := acquireFileLock(ctx, target+".lock")
	if err != nil {
		return mapLockOrTempErr(path, err)
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()

	if err := ensureSameTarget(path, target, "while locking"); err != nil {
		return err
	}

	snapshot, err := LoadSnapshot(ctx, target)
	if err != nil {
		return err
	}

	tmp, err := createTempFile(filepath.Dir(target), filepath.Base(target))
	if err != nil {
		return mapLockOrTempErr(path, err)
	}
	tmpName := tmp.Name()
	tmpHeld := true
	defer func() {
		if tmpHeld {
			err = errors.Join(err, tmp.Close(), os.Remove(tmpName))
		}
	}()

	if err := preflightReplaceable(target); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("refresh credentials canceled: %w", ctx.Err())
	default:
	}

	current := append([]byte(nil), snapshot.Data...)
	if !json.Valid(current) {
		return fmt.Errorf("current credentials %s: %w", path, ErrInvalidJSON)
	}

	edit, err := prepare(current)
	if err != nil {
		return fmt.Errorf("prepare credentials %s: %w", path, err)
	}
	if edit == nil {
		return nil
	}

	if err := ensureSameTarget(path, target, "before commit"); err != nil {
		return err
	}

	latest, err := LoadSnapshot(context.WithoutCancel(ctx), target)
	if err != nil {
		return err
	}
	latestData := append([]byte(nil), latest.Data...)
	if !json.Valid(latestData) {
		return fmt.Errorf("current credentials %s: %w", path, ErrInvalidJSON)
	}

	updated, err := edit(latestData)
	if err != nil {
		return fmt.Errorf("mutate credentials %s: %w", path, err)
	}
	if !json.Valid(updated) {
		return fmt.Errorf("updated credentials %s: %w", path, ErrInvalidJSON)
	}
	if bytes.Equal(updated, latestData) {
		return nil
	}

	tmpHeld = false
	return commitPreparedTemp(tmp, target, updated, latest.Mode, func() error {
		return ensureSameTarget(path, target, "before rename")
	})
}

func mapLockOrTempErr(path string, err error) error {
	if isNotWritableFSError(err) {
		return fmt.Errorf("%w: %s: %w", ErrNotWritable, path, err)
	}
	return err
}
