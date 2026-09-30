package config_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_Unix_symlink_failure_restores_directory_or_selects_new_config(t *testing.T) {
	for _, restoreFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "restore fails"}[restoreFails], func(t *testing.T) {
			// Given
			base := t.TempDir()
			legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
			writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
			writeMigrationFile(t, filepath.Join(legacy, "server.env"), "private fixture")
			logger, logs := migrationLogger()
			rename := func(from, to string) error {
				if from == next && restoreFails {
					return os.ErrPermission
				}
				return os.Rename(from, to)
			}
			ops := config.MigrationHooks(rename, func(string, string) error { return os.ErrPermission }, os.Link)

			// When
			cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", ops)

			// Then
			if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
				t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
			}
			selected, missing := legacy, next
			levels := []slog.Level{slog.LevelWarn, slog.LevelWarn}
			if restoreFails {
				selected, missing = next, legacy
				levels = []slog.Level{slog.LevelWarn, slog.LevelError}
			}
			requireMigrationFile(t, filepath.Join(selected, "config.yaml"), legacyYAML)
			requireMigrationFile(t, filepath.Join(selected, "server.env"), "private fixture")
			requireMissing(t, missing)
			requireLogLevels(t, logs, levels...)
		})
	}
}

func Test_Load_Unix_rename_failure_falls_back_without_changing_config_errors(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "malformed"}[malformed], func(t *testing.T) {
			// Given
			base := t.TempDir()
			legacy := filepath.Join(base, "claude-usage-widget", "config.yaml")
			content := legacyYAML
			if malformed {
				content = "providers: ["
			}
			writeMigrationFile(t, legacy, content)
			logger, logs := migrationLogger()
			ops := config.MigrationHooks(func(string, string) error { return os.ErrPermission }, os.Symlink, os.Link)

			// When
			cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", ops)

			// Then
			if malformed {
				if !errors.Is(err, config.ErrMalformedConfig) {
					t.Fatalf("error=%v, want malformed config", err)
				}
			} else if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
				t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
			}
			requireMigrationFile(t, legacy, content)
			requireMissing(t, filepath.Join(base, "headroom"))
			requireLogLevels(t, logs, slog.LevelWarn, slog.LevelWarn)
		})
	}
}

func Test_Load_Unix_concurrent_migration_does_not_warn(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
	writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
	logger, logs := migrationLogger()
	ops := config.MigrationHooks(func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("headroom", from); err != nil {
			t.Fatal(err)
		}
		return os.ErrNotExist
	}, os.Symlink, os.Link)

	// When
	cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", ops)

	// Then
	if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
		t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
	}
	requireMigrationFile(t, filepath.Join(next, "config.yaml"), legacyYAML)
	requireLogLevels(t, logs)
}

func Test_Load_Unix_explicit_path_uses_matching_basename_across_migration(t *testing.T) {
	for _, parent := range []string{"claude-usage-widget", "headroom"} {
		for _, source := range []string{"flag", "env"} {
			t.Run(parent+"/"+source, func(t *testing.T) {
				// Given
				base := t.TempDir()
				other := "headroom"
				if parent == other {
					other = "claude-usage-widget"
				}
				path := filepath.Join(base, parent, "custom.yaml")
				writeMigrationFile(t, filepath.Join(base, other, "custom.yaml"), legacyYAML)
				opts := config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}}
				if source == "flag" {
					opts.Args = []string{"--config", path}
				} else {
					opts.Env = append(opts.Env, "USAGE_CONFIG="+path)
				}
				ops := config.MigrationHooks(func(string, string) error { return os.ErrPermission }, os.Symlink, os.Link)

				// When
				cfg, err := config.LoadWithMigration(context.Background(), opts, "linux", ops)

				// Then
				if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
					t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
				}
				requireMissing(t, filepath.Dir(path))
				requireMigrationFile(t, filepath.Join(base, other, "custom.yaml"), legacyYAML)
			})
		}
	}
}
