package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_DefaultPath_all_non_Windows_platforms_use_XDG_or_HOME(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd", "plan9"} {
		for _, xdg := range []bool{false, true} {
			t.Run(goos+map[bool]string{true: "/XDG", false: "/HOME"}[xdg], func(t *testing.T) {
				// Given
				home := t.TempDir()
				env := []string{"HOME=" + home}
				base := filepath.Join(home, ".config")
				if xdg {
					base = t.TempDir()
					env = append(env, "XDG_CONFIG_HOME="+base)
				}

				// When
				path, err := config.DefaultPath(goos, env)

				// Then
				if err != nil || path != filepath.Join(base, "headroom", "config.yaml") {
					t.Fatalf("path=%q, err=%v", path, err)
				}
			})
		}
	}
}

func Test_Load_missing_base_only_errors_when_default_is_needed(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, source := range []string{"default", "flag", "env"} {
			t.Run(goos+"/"+source, func(t *testing.T) {
				// Given
				path := filepath.Join(t.TempDir(), "config.yaml")
				writeMigrationFile(t, path, legacyYAML)
				opts := config.LoadOptions{}
				if source == "flag" {
					opts.Args = []string{"--config", path}
				}
				if source == "env" {
					opts.Env = []string{"USAGE_CONFIG=" + path}
				}

				// When
				cfg, err := config.LoadForOS(context.Background(), opts, goos)

				// Then
				if source == "default" {
					if !errors.Is(err, config.ErrInvalidConfig) {
						t.Fatalf("error=%v, want invalid config", err)
					}
				} else if err != nil || cfg.ListenAddr != "127.0.0.1:7123" {
					t.Fatalf("explicit config lost: %q, %v", cfg.ListenAddr, err)
				}
			})
		}
	}
}

func Test_Load_fresh_default_config_uses_defaults_without_creating_directories(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			// Given
			base := t.TempDir()
			opts := config.LoadOptions{Env: []string{"HOME=" + base, "APPDATA=" + base}}

			// When
			requireMigrationLoad(t, opts, goos, "127.0.0.1:7823")

			// Then
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unexpected writes: %v, %v", entries, err)
			}
		})
	}
}
