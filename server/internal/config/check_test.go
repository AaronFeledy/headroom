package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Check_matches_Load_with_effective_overrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	data := []byte("poll_interval: 2m\nproviders:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        config_dir: ~/.claude\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, enable := range []string{"false", "true"} {
		t.Run(enable, func(t *testing.T) {
			opts := config.LoadOptions{Args: []string{"--config", relative, "--poll-interval", "3m"}, Env: []string{"HOME=" + home, "USAGE_PROVIDER_CLAUDE_ENABLED=" + enable, "USAGE_POLL_INTERVAL=4m"}}
			loaded, loadErr := config.Load(context.Background(), opts)
			checked, checkErr := config.Check(data, opts)
			if enable == "true" {
				if !errors.Is(loadErr, config.ErrInvalidConfig) || !errors.Is(checkErr, config.ErrInvalidConfig) {
					t.Fatalf("collision errors: Load=%v Check=%v", loadErr, checkErr)
				}
				return
			}
			if loadErr != nil || checkErr != nil {
				t.Fatalf("Load=%v Check=%v", loadErr, checkErr)
			}
			if !reflect.DeepEqual(loaded, checked) || checked.ConfigPath != path {
				t.Fatalf("effective config mismatch: %+v / %+v", loaded, checked)
			}
			resolved, err := config.ResolvePath(opts)
			if err != nil || resolved != path {
				t.Fatalf("resolved=%q err=%v", resolved, err)
			}
		})
	}
}

func Test_Check_resolves_relative_credentials_path_against_server_directory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	serverDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(serverDir, "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("providers:\n  claude:\n    credentials_path: claude/.credentials.json\n    accounts:\n      - id: work\n        config_dir: " + filepath.Join(serverDir, "claude") + "\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := config.LoadOptions{Args: []string{"--config", path}, Env: []string{"HOME=" + home}}

	_, otherDir := config.Check(data, base)
	server := base
	server.Dir = serverDir
	_, sameDir := config.Check(data, server)

	if otherDir != nil {
		t.Fatalf("relative path resolved outside the server directory collided: %v", otherDir)
	}
	if !errors.Is(sameDir, config.ErrDuplicateAccountDir) {
		t.Fatalf("server directory collision = %v", sameDir)
	}
}
