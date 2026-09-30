package config_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_Windows_copies_config_and_keeps_legacy_files(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
	next := filepath.Join(base, "Headroom", "config.yaml")
	writeMigrationFile(t, legacy, legacyYAML)
	settings := filepath.Join(base, "ClaudeUsageWidget", "settings.json")
	writeMigrationFile(t, settings, "legacy desktop settings")
	logger, logs := migrationLogger()

	// When
	requireMigrationLoad(t, config.LoadOptions{Env: []string{"APPDATA=" + base}, Logger: logger}, "windows", "127.0.0.1:7123")

	// Then
	requireMigrationFile(t, legacy, legacyYAML)
	requireMigrationFile(t, next, legacyYAML)
	requireMigrationFile(t, settings, "legacy desktop settings")
	oldInfo, oldErr := os.Stat(legacy)
	newInfo, newErr := os.Stat(next)
	if oldErr != nil || newErr != nil || os.SameFile(oldInfo, newInfo) {
		t.Fatalf("legacy was not independently copied: %v, %v", oldErr, newErr)
	}
	requireLogLevels(t, logs, slog.LevelInfo)
	entries, err := os.ReadDir(filepath.Dir(next))
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Fatalf("temporary file left behind: %v, %v", entries, err)
	}
}

func Test_Load_Windows_existing_new_config_wins_without_copy_or_warning(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
	next := filepath.Join(base, "Headroom", "config.yaml")
	writeMigrationFile(t, legacy, legacyYAML)
	writeMigrationFile(t, next, nextYAML)
	logger, logs := migrationLogger()

	// When
	requireMigrationLoad(t, config.LoadOptions{Env: []string{"APPDATA=" + base}, Logger: logger}, "windows", "127.0.0.1:7456")

	// Then
	requireMigrationFile(t, legacy, legacyYAML)
	requireMigrationFile(t, next, nextYAML)
	requireLogLevels(t, logs)
}

func Test_Load_Windows_explicit_path_does_not_copy(t *testing.T) {
	for _, parent := range []string{"ClaudeUsageWidget", "Headroom"} {
		for _, source := range []string{"flag", "env"} {
			t.Run(parent+"/"+source, func(t *testing.T) {
				// Given
				base := t.TempDir()
				legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
				next := filepath.Join(base, "Headroom", "config.yaml")
				writeMigrationFile(t, legacy, legacyYAML)
				path := filepath.Join(base, parent, "config.yaml")
				opts := config.LoadOptions{Env: []string{"APPDATA=" + base}}
				if source == "flag" {
					opts.Args = []string{"--config", path}
				} else {
					opts.Env = append(opts.Env, "USAGE_CONFIG="+path)
				}
				want := "127.0.0.1:7123"
				if parent == "Headroom" {
					want = "127.0.0.1:7823"
				}

				// When
				requireMigrationLoad(t, opts, "windows", want)

				// Then
				requireMigrationFile(t, legacy, legacyYAML)
				requireMissing(t, next)
			})
		}
	}
}

func Test_Load_Windows_concurrent_new_config_is_not_overwritten(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
	next := filepath.Join(base, "Headroom", "config.yaml")
	writeMigrationFile(t, legacy, legacyYAML)
	logger, logs := migrationLogger()
	ops := config.MigrationHooks(os.Rename, os.Symlink, func(temp, target string) error {
		writeMigrationFile(t, target, nextYAML)
		return os.Link(temp, target)
	})

	// When
	cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"APPDATA=" + base}, Logger: logger}, "windows", ops)

	// Then
	if err != nil || cfg.ListenAddr != "127.0.0.1:7456" {
		t.Fatalf("concurrent config lost: %q, %v", cfg.ListenAddr, err)
	}
	requireMigrationFile(t, legacy, legacyYAML)
	requireMigrationFile(t, next, nextYAML)
	requireLogLevels(t, logs)
	entries, err := os.ReadDir(filepath.Dir(next))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v, %v", entries, err)
	}
}

func Test_Load_Windows_copy_failure_falls_back_to_legacy(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy := filepath.Join(base, "ClaudeUsageWidget", "config.yaml")
	writeMigrationFile(t, legacy, legacyYAML)
	logger, logs := migrationLogger()
	ops := config.MigrationHooks(os.Rename, os.Symlink, func(string, string) error { return os.ErrPermission })

	// When
	cfg, err := config.LoadWithMigration(context.Background(), config.LoadOptions{Env: []string{"APPDATA=" + base}, Logger: logger}, "windows", ops)

	// Then
	if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
		t.Fatalf("legacy config lost: %q, %v", cfg.ListenAddr, err)
	}
	requireMigrationFile(t, legacy, legacyYAML)
	requireMissing(t, filepath.Join(base, "Headroom", "config.yaml"))
	requireLogLevels(t, logs, slog.LevelWarn, slog.LevelWarn)
}
