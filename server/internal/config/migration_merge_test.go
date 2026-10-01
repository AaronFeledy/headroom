package config_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_Unix_merge_symlink_failure_keeps_moved_files(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
	writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
	writeMigrationFile(t, filepath.Join(legacy, "server.env"), "private fixture")
	writeMigrationFile(t, filepath.Join(next, "settings.json"), "desktop fixture")
	logger, logs := migrationLogger()
	ops := config.MigrationHooks(os.Rename, func(string, string) error { return os.ErrPermission }, os.Link)

	// When
	cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", ops)

	// Then
	if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
		t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
	}
	requireMigrationFile(t, filepath.Join(next, "config.yaml"), legacyYAML)
	requireMigrationFile(t, filepath.Join(next, "server.env"), "private fixture")
	requireMigrationFile(t, filepath.Join(next, "settings.json"), "desktop fixture")
	requireMissing(t, legacy)
	requireLogLevels(t, logs, slog.LevelWarn)
}

func Test_Load_Unix_merge_rename_failure_stops_in_sorted_order(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
	writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
	writeMigrationFile(t, filepath.Join(legacy, "server.env"), "private fixture")
	writeMigrationFile(t, filepath.Join(legacy, "z-last"), "last fixture")
	writeMigrationFile(t, filepath.Join(next, "settings.json"), "desktop fixture")
	logger, logs := migrationLogger()
	var attempted []string
	ops := config.MigrationHooks(func(from, to string) error {
		attempted = append(attempted, filepath.Base(from))
		if filepath.Base(from) == "server.env" {
			return os.ErrPermission
		}
		return os.Rename(from, to)
	}, os.Symlink, os.Link)

	// When
	cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", ops)

	// Then
	if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
		t.Fatalf("config lost: %q, %v", cfg.ListenAddr, err)
	}
	if len(attempted) != 2 || attempted[0] != "config.yaml" || attempted[1] != "server.env" {
		t.Fatalf("rename order=%v", attempted)
	}
	requireMigrationFile(t, filepath.Join(next, "config.yaml"), legacyYAML)
	requireMigrationFile(t, filepath.Join(legacy, "server.env"), "private fixture")
	requireMigrationFile(t, filepath.Join(legacy, "z-last"), "last fixture")
	requireMissing(t, filepath.Join(next, "server.env"))
	requireMissing(t, filepath.Join(next, "z-last"))
	requireLogLevels(t, logs, slog.LevelWarn)
}
