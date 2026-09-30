package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_migrates_Unix_directory_and_preserves_legacy_references(t *testing.T) {
	for _, source := range []string{"default", "flag", "env"} {
		t.Run(source, func(t *testing.T) {
			// Given
			base := t.TempDir()
			legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
			writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
			writeMigrationFile(t, filepath.Join(legacy, "server.env"), "fixture=private\n")
			writeMigrationFile(t, filepath.Join(legacy, "nested", "other"), "other bytes")
			logger, logs := migrationLogger()
			opts := config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}
			if source == "flag" {
				opts.Args = []string{"--config", legacy + "/../claude-usage-widget/config.yaml"}
			}
			if source == "env" {
				opts.Env = append(opts.Env, "USAGE_CONFIG="+filepath.Join(legacy, "config.yaml"))
			}

			// When
			requireMigrationLoad(t, opts, "linux", "127.0.0.1:7123")

			// Then
			target, err := os.Readlink(legacy)
			if err != nil || target != "headroom" {
				t.Fatalf("legacy link=%q, err=%v", target, err)
			}
			for _, dir := range []string{legacy, next} {
				requireMigrationFile(t, filepath.Join(dir, "config.yaml"), legacyYAML)
				requireMigrationFile(t, filepath.Join(dir, "server.env"), "fixture=private\n")
				requireMigrationFile(t, filepath.Join(dir, "nested", "other"), "other bytes")
			}
			for path, mode := range map[string]os.FileMode{next: 0o700, filepath.Join(next, "config.yaml"): 0o600, filepath.Join(next, "server.env"): 0o600} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("mode of %s: info=%v, err=%v", path, info, err)
				}
			}
			requireLogLevels(t, logs, slog.LevelInfo)
		})
	}
}

func Test_Load_Unix_migration_is_idempotent(t *testing.T) {
	// Given
	base := t.TempDir()
	legacy := filepath.Join(base, "claude-usage-widget", "config.yaml")
	writeMigrationFile(t, legacy, legacyYAML)
	logger, logs := migrationLogger()
	opts := config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}
	requireMigrationLoad(t, opts, "linux", "127.0.0.1:7123")
	logs.Reset()

	// When
	requireMigrationLoad(t, opts, "linux", "127.0.0.1:7123")

	// Then
	requireMigrationFile(t, legacy, legacyYAML)
	target, err := os.Readlink(filepath.Dir(legacy))
	if err != nil || target != "headroom" {
		t.Fatalf("legacy link=%q, err=%v", target, err)
	}
	requireLogLevels(t, logs)
}

func Test_Load_Unix_existing_new_directory_prevents_migration(t *testing.T) {
	for _, newConfig := range []bool{true, false} {
		t.Run(map[bool]string{true: "new wins", false: "legacy fallback"}[newConfig], func(t *testing.T) {
			// Given
			base := t.TempDir()
			legacy, next := filepath.Join(base, "claude-usage-widget"), filepath.Join(base, "headroom")
			writeMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
			if err := os.Mkdir(next, 0o700); err != nil {
				t.Fatal(err)
			}
			want := "127.0.0.1:7123"
			if newConfig {
				writeMigrationFile(t, filepath.Join(next, "config.yaml"), nextYAML)
				want = "127.0.0.1:7456"
			}
			logger, logs := migrationLogger()

			// When
			requireMigrationLoad(t, config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", want)

			// Then
			info, err := os.Lstat(legacy)
			if err != nil || !info.IsDir() {
				t.Fatalf("legacy directory changed: %v", err)
			}
			requireMigrationFile(t, filepath.Join(legacy, "config.yaml"), legacyYAML)
			if newConfig {
				requireMigrationFile(t, filepath.Join(next, "config.yaml"), nextYAML)
			} else {
				requireMissing(t, filepath.Join(next, "config.yaml"))
			}
			requireLogLevels(t, logs, slog.LevelWarn)
		})
	}
}

func Test_Load_Unix_user_managed_legacy_symlink_is_untouched(t *testing.T) {
	// Given
	base := t.TempDir()
	elsewhere := filepath.Join(base, "custom")
	writeMigrationFile(t, filepath.Join(elsewhere, "config.yaml"), legacyYAML)
	legacy := filepath.Join(base, "claude-usage-widget")
	if err := os.Symlink(elsewhere, legacy); err != nil {
		t.Fatal(err)
	}
	logger, logs := migrationLogger()

	// When
	requireMigrationLoad(t, config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}, "linux", "127.0.0.1:7123")

	// Then
	target, err := os.Readlink(legacy)
	if err != nil || target != elsewhere {
		t.Fatalf("user link changed: %q, %v", target, err)
	}
	requireMissing(t, filepath.Join(base, "headroom"))
	requireLogLevels(t, logs, slog.LevelWarn)
}

func Test_Load_Unix_unrelated_explicit_path_leaves_default_directories_untouched(t *testing.T) {
	for _, source := range []string{"flag", "env"} {
		t.Run(source, func(t *testing.T) {
			// Given
			base := t.TempDir()
			legacy := filepath.Join(base, "claude-usage-widget", "config.yaml")
			custom := filepath.Join(base, "custom", "config.yaml")
			writeMigrationFile(t, legacy, legacyYAML)
			writeMigrationFile(t, custom, nextYAML)
			logger, logs := migrationLogger()
			opts := config.LoadOptions{Env: []string{"XDG_CONFIG_HOME=" + base}, Logger: logger}
			if source == "flag" {
				opts.Args = []string{"--config", custom}
				opts.Env = append(opts.Env, "USAGE_CONFIG="+legacy)
			} else {
				opts.Env = append(opts.Env, "USAGE_CONFIG="+custom)
			}

			// When
			requireMigrationLoad(t, opts, "linux", "127.0.0.1:7456")

			// Then
			requireMigrationFile(t, legacy, legacyYAML)
			info, err := os.Lstat(filepath.Dir(legacy))
			if err != nil || !info.IsDir() {
				t.Fatalf("legacy changed: %v", err)
			}
			requireMissing(t, filepath.Join(base, "headroom"))
			requireLogLevels(t, logs)
		})
	}
}
