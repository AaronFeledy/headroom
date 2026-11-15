package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Run_check_config_validates_without_starting(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		args       []string
		want       error
	}{
		{"valid", "listen_addr: 127.0.0.1:7823\n", nil, nil},
		{"unsafe bind", "listen_addr: 0.0.0.0:7823\n", nil, api.ErrUnsafeBind},
		{"invalid", "poll_interval: 0s\n", nil, config.ErrInvalidConfig},
		{"malformed", "providers: [\n", nil, config.ErrMalformedConfig},
		{"TLS", "tls: invalid\n", nil, config.ErrInvalidConfig},
		{"desktop", "", []string{"--desktop-session"}, config.ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("APPDATA", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			if tc.want == nil && runtime.GOOS == "linux" {
				tc.body += "ssh_access: true\n"
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			args := append([]string{"--check-config", "--config", path}, tc.args...)
			err := runContext(context.Background(), args, []string{"USAGE_PROVIDER_UNKNOWN_ENABLED=true"}, discardLogger(), desktopSessionOptions{
				output: &output, homeDir: home,
				listen: func(string, string) (net.Listener, error) {
					t.Error("check-config attempted to listen")
					return nil, errors.New("unexpected listen")
				},
				ready: func(Ready) error {
					t.Error("check-config acknowledged a server start")
					return nil
				},
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want == nil {
				if output.String() != "config ok: "+path+"\n" {
					t.Fatalf("output=%q", output.String())
				}
			} else if output.Len() != 0 {
				t.Fatalf("invalid config printed success: %q", output.String())
			}
		})
	}
}
