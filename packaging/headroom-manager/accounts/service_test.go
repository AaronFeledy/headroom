package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

type fakeService struct {
	record    contract.ManagedServiceRecord
	err       error
	restarts  int
	rollbacks int
	restart   func(context.Context, func() error) error
}

func (s *fakeService) Read() (contract.ManagedServiceRecord, error) { return s.record, s.err }
func (s *fakeService) Restart(ctx context.Context, r contract.ManagedServiceRecord, rollback func() error) (contract.ManagedServiceRecord, error) {
	s.restarts++
	if s.restart != nil {
		if err := s.restart(ctx, func() error { s.rollbacks++; return rollback() }); err != nil {
			return contract.ManagedServiceRecord{}, err
		}
	}
	return r, nil
}

func TestConfigSelection(t *testing.T) {
	for _, mode := range []string{"explicit relative", "receipt", "missing receipt", "no service", "invalid receipt", "not ready", "missing path", "explicit invalid receipt", "explicit same overrides", "explicit different overrides"} {
		t.Run(mode, func(t *testing.T) {
			o, out, path := fixture(t)
			base, name := os.Getenv("XDG_CONFIG_HOME"), "headroom"
			if runtime.GOOS == "windows" {
				base, name = os.Getenv("APPDATA"), "Headroom"
			}
			defaultPath, err := filepath.Abs(filepath.Join(base, name, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			s := &fakeService{record: contract.ManagedServiceRecord{Ready: true, ConfigPath: path, Arguments: []string{"--poll-interval", "nonsense"}}}
			o.Service = s
			o.Args = []string{"list"}
			wantPath := defaultPath
			wantConfigFlag := false
			wantValidation := false
			switch mode {
			case "explicit relative":
				o.Getwd = func() (string, error) { return filepath.Dir(path), nil }
				o.Args = []string{"--config", "relative.yaml", "list"}
				wantPath = filepath.Join(filepath.Dir(path), "relative.yaml")
			case "receipt":
				s.record.Arguments = nil
				wantPath = path
			case "missing receipt":
				s.err = os.ErrNotExist
			case "no service":
				o.Service = nil
			case "invalid receipt":
				s.err = errors.New("unverifiable secret detail")
				wantConfigFlag = true
			case "not ready":
				s.record.Ready = false
				wantConfigFlag = true
			case "missing path":
				s.record.ConfigPath = ""
				wantConfigFlag = true
			case "explicit invalid receipt":
				s.err = errors.New("invalid")
				o.Args = []string{"list", "--config", path}
				wantPath = path
			case "explicit same overrides":
				o.Args = []string{"list", "--config", path}
				wantValidation = true
			case "explicit different overrides":
				o.Args = []string{"list", "--config", path + ".other"}
				wantPath = path + ".other"
			}
			err = Run(context.Background(), o)
			if wantConfigFlag {
				if err == nil || !strings.Contains(err.Error(), "--config") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if wantValidation {
				var validation *accountconfig.ValidationError
				if !errors.As(err, &validation) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), wantPath) {
				t.Fatalf("output = %s", out)
			}
		})
	}
}

func TestReceiptEnvironmentValidatesBeforeLogin(t *testing.T) {
	o, _, path := fixture(t)
	o.Service = &fakeService{record: contract.ManagedServiceRecord{Ready: true, ConfigPath: path, Environment: []string{"USAGE_POLL_INTERVAL=invalid"}}}
	o.Args = []string{"add", "claude", "work"}
	var validation *accountconfig.ValidationError
	if err := Run(context.Background(), o); !errors.As(err, &validation) {
		t.Fatalf("error = %v", err)
	}
	if contents(t, path) != "" {
		t.Fatal("config was written")
	}
}

func TestRestartPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, supervisor  string
		outcome           restartOutcomeKind
		absent, different bool
		calls             int
	}{
		{"supervised", "systemd-user", restarted, false, false, 1},
		{"different supervised file", "systemd-user", otherConfig, false, true, 0},
		{"different unsupervised file", "", otherConfig, false, true, 0},
		{"unsupervised", "", foregroundServe, false, false, 0},
		{"no receipt", "", manualRestart, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, path := fixture(t)
			write(t, path, "# original\npoll_interval: 60s\n")
			s := &fakeService{record: contract.ManagedServiceRecord{Ready: true, ConfigPath: path, Supervisor: tc.supervisor}}
			if tc.absent {
				s.err = os.ErrNotExist
			}
			if tc.different {
				s.record.ConfigPath = path + ".service"
			}
			o.Service = s
			target, record, err := o.target(path)
			if err != nil {
				t.Fatal(err)
			}
			change, err := accountconfig.Add(context.Background(), target, accountconfig.Account{ID: "work", ConfigDir: filepath.Join(filepath.Dir(path), ".claude-work"), Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := o.restart(context.Background(), selection{target: target, record: record}, change)
			if err != nil {
				t.Fatal(err)
			}
			if s.restarts != tc.calls || s.rollbacks != 0 || outcome != tc.outcome {
				t.Fatalf("restarts %d; outcome %v, want %v", s.restarts, outcome, tc.outcome)
			}
			if tc.different {
				message := renderRestart(outcome, record, change.Path)
				for _, token := range []string{record.ConfigPath, change.Path} {
					if !strings.Contains(message, token) {
						t.Fatalf("missing path %q in output %q", token, message)
					}
				}
			}
			wantConfig := "# original\npoll_interval: 60s\nproviders:\n  claude:\n    accounts:\n      - id: work\n        config_dir: " + filepath.Join(filepath.Dir(path), ".claude-work") + "\n"
			if contents(t, path) != wantConfig {
				t.Fatalf("config = %q, want %q", contents(t, path), wantConfig)
			}
		})
	}
}
