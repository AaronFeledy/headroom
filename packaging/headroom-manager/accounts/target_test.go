package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestReceiptWorkingDirectoryControlsCredentialValidation(t *testing.T) {
	for _, mode := range []string{"receipt selected", "explicit matching", "explicit different"} {
		t.Run(mode, func(t *testing.T) {
			o, _, path := fixture(t)
			working := filepath.Join(filepath.Dir(path), "server-working")
			dir := filepath.Join(working, "work")
			write(t, filepath.Join(dir, ".credentials.json"), "synthetic credentials")
			const before = "providers:\n  claude:\n    credentials_path: work/.credentials.json\n"
			write(t, path, before)
			s := &fakeService{record: contract.ManagedServiceRecord{Ready: true, ConfigPath: path, WorkingDirectory: working}}
			o.Service = s
			o.Args = []string{"add", "claude", "work", "--config-dir", dir}
			if mode != "receipt selected" {
				o.Args = append(o.Args, "--config", path)
			}
			if mode == "explicit different" {
				s.record.ConfigPath = path + ".managed"
			}
			probes := 0
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				probes++
				return accountconfig.SignIn{State: "signed_in", SourceKind: "cli"}
			}
			err := Run(context.Background(), o)
			if mode == "explicit different" {
				if err != nil {
					t.Fatal(err)
				}
				items, err := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
				if err != nil || len(items) != 1 || items[0].ConfigDir != dir || probes != 1 {
					t.Fatalf("accounts = %+v; probes %d; error %v", items, probes, err)
				}
				return
			}
			if !errors.Is(err, accountconfig.ErrDuplicateConfigDir) || probes != 0 || s.restarts != 0 || contents(t, path) != before {
				t.Fatalf("error = %v; probes %d; restarts %d; config %q", err, probes, s.restarts, contents(t, path))
			}
		})
	}
}
