package config

import (
	"context"
	"path/filepath"
	"testing"
)

func Test_Claude_reset_state_uses_stable_default_config_base(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			env := []string{"HOME=" + home, "APPDATA=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "xdg")}
			defaultPath, err := DefaultPath(goos, env)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(filepath.Dir(defaultPath), "claude-reset-state")
			for _, args := range [][]string{
				{},
				{"--config", filepath.Join(home, "other", "config.yaml"), "--listen-addr", "127.0.0.1:9944", "--auth-token", "fixture"},
			} {
				cfg, err := loadForOS(context.Background(), LoadOptions{Args: args, Env: env}, goos, migrationOps{})
				if err != nil || cfg.ResetStateDir != want {
					t.Fatalf("state directory=%q, want %q, error=%v", cfg.ResetStateDir, want, err)
				}
			}
		})
	}
}
