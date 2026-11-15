package credstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const commitRetries = 4

var commitRetryBaseDelay = 50 * time.Millisecond

// Refresh locks path's resolved target, rejects known non-writable stores, and
// passes the target's bytes to prepare. prepare may contact the network. It
// returns the edit to save, or nil to leave the file alone. prepare runs at most
// once. The edit is pure and runs once per commit attempt: after a write,
// rename, or sync failure, Refresh re-reads the target, applies the edit to the
// fresh bytes again, and retries up to four more times with short backoff while
// holding the lock. It does no network I/O and must not recursively update the
// same store. ctx is checked while waiting for the lock and immediately before
// prepare; after prepare returns an edit, re-reads and commits use
// context.WithoutCancel. The re-read preserves unrelated changes visible at
// that moment; it is not compare-and-swap against writers that do not take
// Headroom's lock. Known permission or replacement restrictions return
// *NotWritableError before prepare; failures after the exchange are ordinary
// errors.
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
		return mapCreationErr(path, err)
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
		return mapCreationErr(path, err)
	}
	tmpHeld := true
	defer func() {
		if tmpHeld {
			err = errors.Join(err, tmp.Close(), os.Remove(tmp.Name()))
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

	commitCtx := context.WithoutCancel(ctx)
	targetChanged := false
	for attempt := 0; ; attempt++ {
		if err := ensureSameTarget(path, target, "before commit"); err != nil {
			return err
		}

		latest, err := LoadSnapshot(commitCtx, target)
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
		renamed, commitErr := commitPreparedTemp(tmp, target, updated, latest.Mode, func() error {
			if err := ensureSameTarget(path, target, "before rename"); err != nil {
				targetChanged = true
				return err
			}
			return nil
		})
		if commitErr == nil {
			return nil
		}
		if renamed || targetChanged || attempt >= commitRetries {
			return commitErr
		}
		time.Sleep(commitRetryBaseDelay << attempt)
		tmp, err = createTempFile(filepath.Dir(target), filepath.Base(target))
		if err != nil {
			// The exchange already ran: post-exchange failures stay ordinary
			// errors, never a refused-before-exchange ErrNotWritable.
			return errors.Join(commitErr, err)
		}
		tmpHeld = true
	}
}

func mapCreationErr(path string, err error) error {
	if isNotWritableFSError(err) {
		return CreationRefusal(path, err)
	}
	return err
}
