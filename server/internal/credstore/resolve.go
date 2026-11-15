package credstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// resolveFileTarget returns the concrete file path behind path, following
// symlinks. Absolute form is built without filepath.Clean so intermediate
// ".." components are applied after symlink targets are known.
func resolveFileTarget(path string, links int) (string, error) {
	if links > 40 {
		return "", fmt.Errorf("too many file symlinks: %w", os.ErrInvalid)
	}
	abs, err := absPathPreserveDots(path)
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
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parentPath := filepath.Dir(abs)
	if parentPath == abs {
		return abs, nil
	}
	parent, err := resolveFileTarget(parentPath, links)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func absPathPreserveDots(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path: %w", os.ErrInvalid)
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + path, nil
}

func ensureSameTarget(path, frozen, stage string) error {
	current, err := resolveFileTarget(path, 0)
	if err != nil {
		return err
	}
	if current != frozen {
		return fmt.Errorf("file target changed %s: %w", stage, os.ErrInvalid)
	}
	return nil
}
