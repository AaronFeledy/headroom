//go:build !windows

package credstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func ensureDirectory(ctx context.Context, path string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil {
		if !os.IsExist(err) {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("durable directory destination is not a directory")
		}
	}
	// Always sync, including directories left visible by an earlier failed sync
	// or created concurrently. EnsureDirectory visits the full ancestor chain.
	return syncDirectory(filepath.Dir(path))
}

func createDirectory(ctx context.Context, path string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
