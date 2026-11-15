package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

type fakeDesktop struct {
	result  DesktopRestart
	calls   int
	path    string
	restart func(context.Context) error
}

func (d *fakeDesktop) RestartServer(ctx context.Context, path string) (DesktopRestart, error) {
	d.calls++
	d.path = path
	if d.restart != nil {
		if err := d.restart(ctx); err != nil {
			return 0, err
		}
	}
	return d.result, nil
}

func TestDesktopRestartOutcomes(t *testing.T) {
	for _, tc := range []struct {
		result DesktopRestart
		want   restartOutcomeKind
	}{
		{DesktopUnavailable, manualRestart},
		{DesktopNotOwner, manualRestart},
		{DesktopOtherConfig, desktopOtherConfig},
		{DesktopBusy, desktopBusy},
		{DesktopRestarted, desktopRestarted},
	} {
		o, _, path := fixture(t)
		a := savedAccount(t, o, path)
		d := &fakeDesktop{result: tc.result}
		o.Desktop = d
		target := accountconfig.Target{Path: path, Env: o.Env}
		change, err := accountconfig.Remove(context.Background(), target, a.ID)
		if err != nil {
			t.Fatal(err)
		}

		outcome, err := o.restart(context.Background(), selection{target: target}, change)

		if err != nil || outcome != tc.want || d.calls != 1 || d.path != path || contents(t, path) != string(change.After) {
			t.Fatalf("result %v: outcome %v; error %v; desktop %+v", tc.result, outcome, err, d)
		}
	}
}

func TestManagedReceiptTakesPrecedenceOverDesktop(t *testing.T) {
	for _, supervisor := range []string{"", "systemd-user"} {
		o, _, path := fixture(t)
		d := &fakeDesktop{result: DesktopRestarted}
		o.Desktop = d
		o.Service = &fakeService{record: contract.ManagedServiceRecord{Ready: true, ConfigPath: path, Supervisor: supervisor}}
		o.Args = []string{"add", "claude", "work", "--config", path}

		if err := Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}

		if d.calls != 0 {
			t.Fatalf("desktop calls %d", d.calls)
		}
	}
}

func TestDesktopFailureRestoresConfigAndRestartsOnce(t *testing.T) {
	for _, mode := range []string{"restored", "relaunch failed", "unavailable", "busy", "not owner", "other config", "concurrent edit", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			o, out, path := fixture(t)
			savedAccount(t, o, path)
			before := contents(t, path)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			launchErr := &os.PathError{Op: "launch", Path: "synthetic server", Err: os.ErrPermission}
			d := &fakeDesktop{result: DesktopRestarted}
			switch mode {
			case "unavailable":
				d.result = DesktopUnavailable
			case "busy":
				d.result = DesktopBusy
			case "not owner":
				d.result = DesktopNotOwner
			case "other config":
				d.result = DesktopOtherConfig
			}
			d.restart = func(restartCtx context.Context) error {
				if d.calls == 1 {
					if mode == "concurrent edit" {
						write(t, path, "# concurrent edit\n")
					}
					if mode == "canceled" {
						cancel()
					}
					return launchErr
				}
				if restartCtx.Err() != nil {
					t.Fatal("recovery context was canceled")
				}
				if contents(t, path) != before {
					t.Fatal("restarted before restoring config")
				}
				if mode == "relaunch failed" {
					return launchErr
				}
				return nil
			}
			o.Desktop = d
			o.Args = []string{"set", "claude", "work", "--disable", "--config", path}

			err := Run(ctx, o)

			kind, calls, config := rolledBack, 2, before
			if mode == "relaunch failed" || d.result != DesktopRestarted {
				kind = undoneNotRestarted
			}
			cause := error(launchErr)
			if mode == "concurrent edit" {
				kind, calls, config, cause = notUndone, 1, "# concurrent edit\n", accountconfig.ErrConfigChanged
			}
			var failure *restartError
			if !errors.As(err, &failure) || failure.kind != kind || !failure.desktop || !errors.Is(err, cause) || d.calls != calls || contents(t, path) != config || out.Len() != 0 {
				t.Fatalf("error %v; desktop %+v; config %q; output %s", err, d, contents(t, path), out)
			}
		})
	}
}

func TestDesktopUnknownOutcomeKeepsChangeWithoutRetry(t *testing.T) {
	o, out, path := fixture(t)
	savedAccount(t, o, path)
	d := &fakeDesktop{restart: func(context.Context) error {
		return fmt.Errorf("%w: %w", ErrDesktopOutcomeUnknown, context.Canceled)
	}}
	o.Desktop = d
	o.Args = []string{"set", "claude", "work", "--disable", "--config", path}
	before := contents(t, path)

	err := Run(context.Background(), o)

	var failure *restartError
	if !errors.As(err, &failure) || failure.kind != notRestarted || !failure.desktop || !errors.Is(err, ErrDesktopOutcomeUnknown) {
		t.Fatalf("error = %v", err)
	}
	if d.calls != 1 || contents(t, path) == before || out.Len() != 0 {
		t.Fatalf("desktop calls %d; config restored %v; output %s", d.calls, contents(t, path) == before, out)
	}
}
