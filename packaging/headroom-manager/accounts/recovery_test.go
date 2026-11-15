package accounts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestRestartFailureOutcomes(t *testing.T) {
	for _, mode := range []string{"rolled back", "relaunch failed", "restore failed", "restart refused", "canceled restart"} {
		t.Run(mode, func(t *testing.T) {
			o, out, path := fixture(t)
			const before = "# original config\npoll_interval: 60s\n"
			write(t, path, before)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			launchErr := &os.PathError{Op: "launch", Path: "headroom.service", Err: os.ErrPermission}
			loginCalls := 0
			o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
				loginCalls++
				return nil
			}
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				if loginCalls == 0 {
					return accountconfig.SignIn{State: "signed_out"}
				}
				return accountconfig.SignIn{State: "signed_in"}
			}
			s := &fakeService{record: contract.ManagedServiceRecord{Ready: true, Supervisor: "systemd-user", ConfigPath: path}}
			s.restart = func(_ context.Context, rollback func() error) error {
				if mode == "restart refused" {
					return launchErr
				}
				if mode == "restore failed" {
					write(t, path, "# concurrent\n")
				}
				if mode == "canceled restart" {
					cancel()
				}
				if err := rollback(); err != nil {
					return errors.Join(launchErr, err)
				}
				if mode == "relaunch failed" {
					return launchErr
				}
				return errors.Join(contract.ErrRestartRolledBack, launchErr)
			}
			o.Service = s
			o.Args = []string{"add", "claude", "work", "--config", path}
			err := Run(ctx, o)
			wantKind := rolledBack
			var wantCause error = launchErr
			wantRollbacks := 1
			wantConfig := before
			switch mode {
			case "relaunch failed":
				wantKind = undoneNotRestarted
			case "restore failed":
				wantKind, wantCause = notUndone, accountconfig.ErrConfigChanged
				wantConfig = "# concurrent\n"
			case "restart refused":
				wantKind, wantRollbacks = notRestarted, 0
				wantConfig = before + "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: " + filepath.Join(filepath.Dir(path), ".claude-work") + "\n"
			}
			var failure *restartError
			if !errors.As(err, &failure) || failure.kind != wantKind || failure.Path != path || !errors.Is(err, wantCause) {
				t.Fatalf("error = %v; want kind %v, path %q, cause %v", err, wantKind, path, wantCause)
			}
			if wantCause == launchErr {
				var cause *os.PathError
				if !errors.As(err, &cause) || cause != launchErr {
					t.Fatalf("wrapped cause = %v", cause)
				}
			}
			if wantKind == rolledBack && !errors.Is(err, contract.ErrRestartRolledBack) {
				t.Fatalf("lost rollback sentinel: %v", err)
			}
			if wantKind == notRestarted && !strings.Contains(err.Error(), path) {
				t.Fatalf("error missing saved config path: %v", err)
			}
			if loginCalls != 1 || s.restarts != 1 || s.rollbacks != wantRollbacks || contents(t, path) != wantConfig {
				t.Fatalf("restarts %d; config = %q, want %q", s.restarts, contents(t, path), wantConfig)
			}
			if out.Len() != 0 {
				t.Fatalf("false success: %s", out)
			}
		})
	}
}

func TestMissingClaudeExplainsInstallation(t *testing.T) {
	o, _, path := fixture(t)
	t.Setenv("PATH", t.TempDir())
	o.Login = nil
	o.SignIn = func(context.Context, string) accountconfig.SignIn { return accountconfig.SignIn{State: "signed_out"} }
	o.Args = []string{"add", "claude", "work", "--config", path}
	err := Run(context.Background(), o)
	if !errors.Is(err, errClaudeNotFound) {
		t.Fatalf("error = %v", err)
	}
	if contents(t, path) != "" {
		t.Fatal("config changed before sign-in")
	}
}

func TestAddUsesCurrentConfigAfterLogin(t *testing.T) {
	o, _, path := fixture(t)
	loggedIn := false
	o.SignIn = func(context.Context, string) accountconfig.SignIn {
		if loggedIn {
			return accountconfig.SignIn{State: "signed_in"}
		}
		return accountconfig.SignIn{State: "signed_out"}
	}
	o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
		loggedIn = true
		write(t, path, "# written during login\npoll_interval: 90s\n")
		return nil
	}
	o.Args = []string{"add", "claude", "work", "--config", path}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# written during login", "poll_interval: 90s", "id: work"} {
		if !strings.Contains(contents(t, path), want) {
			t.Fatalf("lost %q", want)
		}
	}
}

func TestSameFileRecognizesConfigSymlink(t *testing.T) {
	o, out, path := fixture(t)
	write(t, path, "# original\n")
	link := filepath.Join(filepath.Dir(path), "linked.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	s := &fakeService{record: contract.ManagedServiceRecord{Ready: true, Supervisor: "systemd-user", ConfigPath: path}}
	o.Service = s
	o.Args = []string{"add", "claude", "work", "--config", link}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if s.restarts != 1 || s.rollbacks != 0 || !strings.Contains(out.String(), link) {
		t.Fatalf("restarts %d; output %s", s.restarts, out)
	}
}
