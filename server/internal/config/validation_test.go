package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func TestLoadRejectsNonpositivePollInterval(t *testing.T) {
	for _, value := range []string{"0s", "-1s"} {
		for _, source := range []string{"yaml", "env", "flag"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				opts := config.LoadOptions{Args: []string{"--config", path}}
				switch source {
				case "yaml":
					if err := os.WriteFile(path, []byte("poll_interval: "+value+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "env":
					opts.Env = []string{"USAGE_POLL_INTERVAL=" + value}
				case "flag":
					opts.Args = append(opts.Args, "--poll-interval", value)
				}
				if _, err := config.Load(context.Background(), opts); !errors.Is(err, config.ErrInvalidConfig) {
					t.Fatalf("Load error = %v, want invalid config", err)
				}
			})
		}
	}
}
