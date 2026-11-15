package credstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func replaceAtomically(path string, data []byte, mode os.FileMode) error {
	tmp, err := createTempFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	return commitPreparedTemp(tmp, path, data, mode, nil)
}

func createTempFile(dir, base string) (*os.File, error) {
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temp credential file: %w", err)
	}
	return tmp, nil
}

func writeTempContents(tmp *os.File, data []byte, mode os.FileMode) error {
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temp credential file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp credential file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp credential file: %w", err)
	}
	return nil
}

func commitPreparedTemp(tmp *os.File, path string, data []byte, mode os.FileMode, beforeRename func() error) (err error) {
	tmpName := tmp.Name()
	open := true
	committed := false
	defer func() {
		if open {
			err = errors.Join(err, tmp.Close())
		}
		if !committed {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()
	if err := writeTempContents(tmp, data, mode); err != nil {
		return err
	}
	open = false
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp credential file: %w", err)
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return err
		}
	}
	if err := replaceExisting(tmpName, path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	committed = true
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}
