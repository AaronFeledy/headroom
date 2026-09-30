package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

func (m configMigration) copyWindows(legacy, next string) {
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		return
	}
	info, err := os.Stat(legacy)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if err := m.copyConfig(legacy, next); err != nil {
		m.log(slog.LevelWarn, "config copy failed", "from", legacy, "to", next, "error", err.Error())
	}
}

func (m configMigration) copyConfig(legacy, next string) (err error) {
	source, err := os.Open(legacy)
	if err != nil {
		return fmt.Errorf("open legacy config: %w", err)
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	if err := os.MkdirAll(filepath.Dir(next), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(next), ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(temp.Name()); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary config: %w", removeErr))
		}
	}()
	// Close before installation: Windows does not allow deleting an open temp.
	_, copyErr := io.Copy(temp, source)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	// CreateHardLink on Windows installs complete bytes and fails if next exists.
	if err := m.ops.link(temp.Name(), next); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("install config: %w", err)
	}
	m.log(slog.LevelInfo, "config copied", "from", legacy, "to", next)
	return nil
}
