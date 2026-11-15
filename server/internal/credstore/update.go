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
func UpdateFile(ctx context.Context, path string, newMode os.FileMode, mutator Mutator) error {
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
	unchanged := func(stage string) error {
		current, err := resolveFileTarget(path, 0)
		if err != nil {
			return err
		}
		if current != target {
			return fmt.Errorf("file target changed %s: %w", stage, os.ErrInvalid)
		}
		return nil
	}
	return lockedReplace(ctx, target, &newMode, func(current []byte) ([]byte, error) {
		if err := unchanged("while locking"); err != nil {
			return nil, err
		}
		updated, err := mutator(current)
		if err != nil {
			return nil, err
		}
		return updated, unchanged("before commit")
	})
}

func resolveFileTarget(path string, links int) (string, error) {
	if links > 40 {
		return "", fmt.Errorf("too many file symlinks: %w", os.ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	info, statErr := os.Lstat(abs)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(abs)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(abs), link)
		}
		return resolveFileTarget(link, links+1)
	}
	parent, err := resolveFileTarget(filepath.Dir(abs), links)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
