package accounts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestAddSkipsLoginAndPreservesSpelling(t *testing.T) {
	o, out, path := fixture(t)
	dir := "~/inside/../account/"
	logins := 0
	o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
		logins++
		return nil
	}
	o.Args = []string{"--config", path, "add", "claude", "work", "--label", "Work", "--config-dir", dir}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if logins != 0 {
		t.Fatalf("login called %d times", logins)
	}
	items, err := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
	if err != nil {
		t.Fatal(err)
	}
	wantDir := os.Getenv("HOME") + dir[1:]
	if len(items) != 1 || items[0].ID != "work" || items[0].Label != "Work" || items[0].ConfigDir != wantDir || !items[0].Enabled {
		t.Fatalf("accounts = %+v", items)
	}
	where := filepath.Join(wantDir, ".credentials.json")
	for _, token := range []string{where, path, "Work"} {
		if !strings.Contains(out.String(), token) {
			t.Fatalf("missing %q in %s", token, out)
		}
	}
}

func TestLoginEnvironmentMatchesStoredDirectory(t *testing.T) {
	o, _, path := fixture(t)
	dir := os.Getenv("HOME") + "/nested/../work/"
	o.Env = append(o.Env, "CLAUDE_CONFIG_DIR=wrong", "CLAUDE_CONFIG_DIR=also-wrong")
	o.SignIn = func(_ context.Context, dir string) accountconfig.SignIn {
		if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil {
			return accountconfig.SignIn{State: "signed_in"}
		}
		return accountconfig.SignIn{State: "signed_out"}
	}
	var loginDir string
	var loginEnv []string
	o.Login = func(_ context.Context, d string, _ io.Reader, _, _ io.Writer, env []string) error {
		loginDir, loginEnv = d, env
		write(t, filepath.Join(d, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"fake-access","refreshToken":"fake-refresh","expiresAt":4102444800000}}`)
		return nil
	}
	o.Args = []string{"add", "claude", "work", "--config", path, "--config-dir", dir}
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	items, err := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || loginDir != dir || items[0].ConfigDir != dir {
		t.Fatalf("login dir %q; accounts %+v", loginDir, items)
	}
	var found []string
	for _, entry := range loginEnv {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			found = append(found, entry)
		}
	}
	if len(found) != 1 || found[0] != "CLAUDE_CONFIG_DIR="+items[0].ConfigDir {
		t.Fatalf("config env = %v", found)
	}
}

func TestAddFailuresLeaveConfigUntouched(t *testing.T) {
	denied := errors.New("synthetic login rejection")
	for _, tc := range []struct {
		name, initial, after string
		loginError           error
		login                bool
	}{
		{"login failure", "signed_out", "", denied, true},
		{"failed reprobe", "signed_out", "signed_out", nil, true},
		{"expired reprobe", "expired", "expired", nil, true},
		{"locked", "locked", "", nil, false},
		{"unreadable", "unreadable", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, path := fixture(t)
			const before = "# preserved\npoll_interval: 60s\n"
			write(t, path, before)
			logins := 0
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				state := tc.initial
				if logins > 0 {
					state = tc.after
				}
				return accountconfig.SignIn{State: state, SourceKind: "cli", SourceName: "synthetic", Checked: []accountconfig.Checked{{Kind: "cli", Name: "synthetic", Status: state}}}
			}
			o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
				logins++
				return tc.loginError
			}
			o.Args = []string{"add", "claude", "work", "--config", path}
			err := Run(context.Background(), o)
			if err == nil {
				t.Fatal("expected error")
			}
			wantLogins := 0
			if tc.login {
				wantLogins = 1
			}
			if logins != wantLogins || contents(t, path) != before {
				t.Fatalf("login %d; config %q", logins, contents(t, path))
			}
			if tc.loginError != nil && !errors.Is(err, tc.loginError) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestHelpAndParsing(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"add", "claude", "work", "--help"}} {
		o, out, _ := fixture(t)
		o.Args = args
		if err := Run(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, token := range []string{"list", "add", "remove", "claude", "--config", "--label", "--config-dir"} {
			if !strings.Contains(got, token) {
				t.Fatalf("help missing %q: %q", token, got)
			}
		}
	}
	for _, tc := range []struct {
		args  []string
		token string
	}{
		{[]string{"add", "grok", "work"}, "claude"},
		{[]string{"remove", "claude"}, "remove"},
		{[]string{"list", "extra"}, "list"},
		{[]string{"list", "--label", "Work"}, "label"},
		{[]string{"add", "claude", "work", "--unknown"}, "unknown"},
	} {
		o, _, _ := fixture(t)
		o.Args = tc.args
		err := Run(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), tc.token) {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
}

func TestListShowsPathStatusAndEmptyHint(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "populated"}[populated], func(t *testing.T) {
			o, out, path := fixture(t)
			if populated {
				write(t, path, "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        config_dir: /synthetic/work\n        enabled: false\n")
			}
			o.Args = []string{"list", "--config", path}
			if err := Run(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if !strings.Contains(got, path) {
				t.Fatalf("missing path in %s", got)
			}
			if populated {
				for _, token := range []string{"work", "Work", "disabled", "/synthetic/work"} {
					if !strings.Contains(got, token) {
						t.Fatalf("missing %q in %s", token, got)
					}
				}
			} else {
				for _, token := range []string{"add", "claude"} {
					if !strings.Contains(got, token) {
						t.Fatalf("missing %q in %s", token, got)
					}
				}
			}
		})
	}
}

func TestRemoveLeavesSignInUntouchedAndNamesKnownIDs(t *testing.T) {
	for _, id := range []string{"work", "missing"} {
		t.Run(id, func(t *testing.T) {
			o, out, path := fixture(t)
			dir := filepath.Join(os.Getenv("HOME"), "work")
			credential := filepath.Join(dir, ".credentials.json")
			write(t, credential, "synthetic credentials")
			write(t, path, "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        config_dir: "+dir+"\n")
			o.Args = []string{"remove", "claude", id, "--config", path}
			err := Run(context.Background(), o)
			if id == "missing" {
				if err == nil || !errors.Is(err, accountconfig.ErrAccountNotFound) {
					t.Fatalf("error = %v", err)
				}
				if !strings.Contains(err.Error(), "work") {
					t.Fatalf("error missing known id: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if contents(t, credential) != "synthetic credentials" {
				t.Fatal("credentials changed")
			}
			for _, token := range []string{"Work", dir, path} {
				if !strings.Contains(out.String(), token) {
					t.Fatalf("missing %q in %s", token, out)
				}
			}
			items, err := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
			if err != nil || len(items) != 0 {
				t.Fatalf("accounts %+v; error %v", items, err)
			}
		})
	}
}
