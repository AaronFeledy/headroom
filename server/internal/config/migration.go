package config

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
)

// Per-load operations allow deterministic failures without global test hooks.
type migrationOps struct {
	rename  func(string, string) error
	symlink func(string, string) error
	link    func(string, string) error
}

type configMigration struct {
	goos   string
	ops    migrationOps
	logger *slog.Logger
}

func (m configMigration) resolve(flags flagValues, envList []string) (string, error) {
	env := parseEnv(envList)
	path := env["USAGE_CONFIG"]
	explicit := path != ""
	if flags.Set["config"] {
		path, explicit = flags.ConfigPath, true
	}
	if !explicit {
		var err error
		path, err = DefaultPath(m.goos, envList)
		if err != nil {
			return "", err
		}
	}
	if m.goos == "windows" && explicit {
		return path, nil
	}
	base, err := configBase(m.goos, env)
	if err != nil {
		// An explicit config never requires HOME or APPDATA.
		return path, nil
	}
	if m.goos == "windows" {
		legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
		m.copyWindows(legacy, path)
		return m.selectDefault(legacy, path), nil
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return path, nil
	}
	legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
	if explicit {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return path, nil
		}
		parent := filepath.Dir(absolute)
		if parent != legacy && parent != next {
			return path, nil
		}
		m.moveUnix(legacy, next)
		other := filepath.Join(next, filepath.Base(absolute))
		if parent == next {
			other = filepath.Join(legacy, filepath.Base(absolute))
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if _, err := os.Stat(other); err == nil {
				if parent == next {
					m.log(slog.LevelWarn, "using legacy config location", "path", other)
				}
				return other, nil
			}
		}
		return path, nil
	}
	m.moveUnix(legacy, next)
	return m.selectDefault(filepath.Join(legacy, "config.yaml"), filepath.Join(next, "config.yaml")), nil
}

func (m configMigration) moveUnix(legacy, next string) {
	if _, err := os.Lstat(next); !os.IsNotExist(err) {
		return
	}
	info, err := os.Lstat(legacy)
	if err != nil || !info.IsDir() {
		return
	}
	if err := m.ops.rename(legacy, next); err != nil {
		old, oldErr := os.Lstat(legacy)
		_, nextErr := os.Lstat(next)
		if nextErr == nil && (os.IsNotExist(oldErr) || (oldErr == nil && old.Mode()&os.ModeSymlink != 0)) {
			return // Another startup already migrated this directory.
		}
		m.log(slog.LevelWarn, "config directory migration failed", "from", legacy, "to", next, "error", err.Error())
		return
	}
	if err := m.ops.symlink(filepath.Base(next), legacy); err != nil {
		m.log(slog.LevelWarn, "config compatibility link failed", "from", legacy, "to", next, "error", err.Error())
		if err := m.ops.rename(next, legacy); err != nil {
			m.log(slog.LevelError, "config directory restore failed", "from", next, "to", legacy, "error", err.Error())
		}
		return
	}
	m.log(slog.LevelInfo, "config directory migrated", "from", legacy, "to", next)
}

func (m configMigration) selectDefault(legacy, next string) string {
	nextInfo, nextErr := os.Stat(next)
	legacyInfo, legacyErr := os.Stat(legacy)
	if nextErr == nil {
		if m.goos != "windows" && legacyErr == nil && !os.SameFile(nextInfo, legacyInfo) {
			m.log(slog.LevelWarn, "legacy config ignored", "path", legacy, "selected", next)
		}
		return next
	}
	if legacyErr == nil {
		m.log(slog.LevelWarn, "using legacy config location", "path", legacy)
		return legacy
	}
	return next
}

func (m configMigration) log(level slog.Level, message string, attrs ...string) {
	if m.logger == nil {
		return
	}
	fields := make([]slog.Attr, 0, len(attrs)/2)
	for i := 0; i < len(attrs); i += 2 {
		fields = append(fields, slog.String(attrs[i], attrs[i+1]))
	}
	m.logger.LogAttrs(context.Background(), level, message, fields...)
}
