package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func savedAccount(t *testing.T, o Options, path string) accountconfig.Account {
	t.Helper()
	a := accountconfig.Account{ID: "work", Label: "Work", ConfigDir: filepath.Join(filepath.Dir(path), "work account") + string(filepath.Separator), Enabled: true}
	if _, err := accountconfig.Add(context.Background(), accountconfig.Target{Path: path, Env: o.Env}, a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoginAlwaysRunsWithoutEditingOrRestarting(t *testing.T) {
	for _, state := range []string{"signed_in", "signed_out", "expired", "locked", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			o, _, path := fixture(t)
			a := savedAccount(t, o, path)
			before := contents(t, path)
			calls, probes := 0, 0
			o.Login = func(_ context.Context, dir string, _ io.Reader, _, _ io.Writer, env []string) error {
				calls++
				if dir != a.ConfigDir || !reflect.DeepEqual(env, loginEnvironment(o.Env, a.ConfigDir)) {
					t.Fatalf("login directory %q; environment %v", dir, env)
				}
				return nil
			}
			o.SignIn = func(_ context.Context, dir string) accountconfig.SignIn {
				probes++
				if dir != a.ConfigDir {
					t.Fatalf("probe directory %q", dir)
				}
				return accountconfig.SignIn{State: state}
			}
			d := &fakeDesktop{result: DesktopRestarted}
			o.Desktop = d
			o.Args = []string{"login", "claude", "work", "--config", path}

			err := Run(context.Background(), o)

			if (err == nil) != (state == "signed_in") || calls != 1 || probes != 1 || d.calls != 0 || contents(t, path) != before {
				t.Fatalf("error %v; logins %d; probes %d; restarts %d; config %q", err, calls, probes, d.calls, contents(t, path))
			}
		})
	}
}

func TestUnknownAccountNamesKnownIDsWithoutLogin(t *testing.T) {
	for _, action := range []string{"login", "set"} {
		t.Run(action, func(t *testing.T) {
			o, _, path := fixture(t)
			savedAccount(t, o, path)
			before := contents(t, path)
			o.Args = []string{action, "claude", "missing", "--config", path}
			if action == "set" {
				o.Args = append(o.Args, "--disable")
			}

			err := Run(context.Background(), o)

			if !errors.Is(err, accountconfig.ErrAccountNotFound) || !strings.Contains(err.Error(), "work") || contents(t, path) != before {
				t.Fatalf("error %v; config %q", err, contents(t, path))
			}
		})
	}
}

func TestSetUpdatesOnlyRequestedFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		label   string
		enabled bool
	}{
		{"label", []string{"--label", "Work Team"}, "Work Team", true},
		{"disable", []string{"--disable"}, "Work", false},
		{"enable", []string{"--enable"}, "Work", true},
		{"both fields", []string{"--label", "Team", "--disable"}, "Team", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, path := fixture(t)
			a := savedAccount(t, o, path)
			if tc.name == "enable" {
				disabled := false
				if _, err := accountconfig.Update(context.Background(), accountconfig.Target{Path: path, Env: o.Env}, a.ID, accountconfig.AccountChanges{Enabled: &disabled}); err != nil {
					t.Fatal(err)
				}
			}
			d := &fakeDesktop{result: DesktopRestarted}
			o.Desktop = d
			o.Args = append([]string{"set", "claude", "work", "--config", path}, tc.flags...)

			if err := Run(context.Background(), o); err != nil {
				t.Fatal(err)
			}

			items, err := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
			a.Label, a.Enabled = tc.label, tc.enabled
			if err != nil || !reflect.DeepEqual(items, []accountconfig.Account{a}) || d.calls != 1 || d.path != path {
				t.Fatalf("accounts %+v; error %v; desktop %+v", items, err, d)
			}
		})
	}
}

func TestSetRejectsMissingOrConflictingChanges(t *testing.T) {
	for _, flags := range [][]string{nil, {"--enable", "--disable"}, {"--label", "invalid\x01label"}} {
		o, _, path := fixture(t)
		savedAccount(t, o, path)
		before := contents(t, path)
		o.Args = append([]string{"set", "claude", "work", "--config", path}, flags...)

		err := Run(context.Background(), o)

		if err == nil || len(flags) == 0 && !errors.Is(err, accountconfig.ErrNoChanges) || contents(t, path) != before {
			t.Fatalf("flags %v: error %v; config %q", flags, err, contents(t, path))
		}
	}
}

func TestListJSONUsesStableFieldsAndEmptyArray(t *testing.T) {
	for _, populated := range []bool{false, true} {
		o, out, path := fixture(t)
		var a accountconfig.Account
		if populated {
			a = savedAccount(t, o, path)
		}
		o.Args = []string{"list", "--json", "--config", path}

		if err := Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}

		var result struct {
			ConfigPath string `json:"config_path"`
			Accounts   []struct {
				ID        string `json:"id"`
				Label     string `json:"label"`
				Enabled   bool   `json:"enabled"`
				ConfigDir string `json:"config_dir"`
				SignIn    struct {
					State      string `json:"state"`
					SourceKind string `json:"source_kind"`
					SourceName string `json:"source_name"`
				} `json:"sign_in"`
			} `json:"accounts"`
		}
		decoder := json.NewDecoder(out)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.ConfigPath != path || result.Accounts == nil || len(result.Accounts) != map[bool]int{false: 0, true: 1}[populated] {
			t.Fatalf("result %+v", result)
		}
		if populated {
			got := result.Accounts[0]
			if got.ID != a.ID || got.Label != a.Label || got.Enabled != a.Enabled || got.ConfigDir != a.ConfigDir || got.SignIn.State != "signed_in" || got.SignIn.SourceKind != "cli" || got.SignIn.SourceName != "synthetic credentials" {
				t.Fatalf("account %+v", got)
			}
		}
	}
}
