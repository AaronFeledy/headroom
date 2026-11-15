package accounts

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestAddSkipAndUnreadableLocations(t *testing.T) {
	for _, tc := range []struct {
		name, kind, state string
	}{
		{"skip keychain", "keychain", "signed_in"},
		{"skip cli", "cli", "signed_in"},
		{"unreadable keychain", "keychain", "unreadable"},
		{"unreadable cli", "cli", "unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, out, path := fixture(t)
			dir, err := accountconfig.DefaultClaudeConfigDir("work")
			if err != nil {
				t.Fatal(err)
			}
			const before = ""
			logins := 0
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				return accountconfig.SignIn{State: tc.state, SourceKind: tc.kind, SourceName: "ignored"}
			}
			o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
				logins++
				return nil
			}
			o.Args = []string{"add", "claude", "work", "--config", path}
			err = Run(context.Background(), o)
			if logins != 0 {
				t.Fatalf("login called %d times", logins)
			}
			if tc.state == "signed_in" {
				if err != nil {
					t.Fatal(err)
				}
				items, listErr := accountconfig.List(context.Background(), accountconfig.Target{Path: path, Env: o.Env})
				if listErr != nil {
					t.Fatal(listErr)
				}
				if len(items) != 1 || items[0].ID != "work" || items[0].ConfigDir != dir || !items[0].Enabled {
					t.Fatalf("accounts = %+v", items)
				}
				if tc.kind == "cli" && !strings.Contains(out.String(), filepath.Join(dir, ".credentials.json")) {
					t.Fatalf("output missing credential path: %s", out)
				}
				if !strings.Contains(out.String(), path) || !strings.Contains(out.String(), "work") {
					t.Fatalf("output missing tokens: %s", out)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if contents(t, path) != before {
				t.Fatalf("config changed: %q", contents(t, path))
			}
			if tc.kind == "cli" && !strings.Contains(err.Error(), filepath.Join(dir, ".credentials.json")) {
				t.Fatalf("error missing credential path: %v", err)
			}
		})
	}
}

func TestAddReprobeCheckedStates(t *testing.T) {
	for _, state := range []string{"signed_out", "expired", "locked", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			o, _, path := fixture(t)
			dir, err := accountconfig.DefaultClaudeConfigDir("work")
			if err != nil {
				t.Fatal(err)
			}
			const before = ""
			logins := 0
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				if logins == 0 {
					return accountconfig.SignIn{State: "signed_out"}
				}
				return accountconfig.SignIn{
					State: state,
					Checked: []accountconfig.Checked{
						{Kind: "keychain", Name: "item", Status: "locked"},
						{Kind: "cli", Name: "file", Status: state},
					},
				}
			}
			o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
				logins++
				return nil
			}
			o.Args = []string{"add", "claude", "work", "--config", path}
			err = Run(context.Background(), o)
			if logins != 1 {
				t.Fatalf("login called %d times", logins)
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if contents(t, path) != before {
				t.Fatalf("config changed: %q", contents(t, path))
			}
			cliWhere := filepath.Join(dir, ".credentials.json")
			for _, token := range []string{dir, cliWhere} {
				if !strings.Contains(err.Error(), token) {
					t.Fatalf("error = %v; missing %q", err, token)
				}
			}
		})
	}
}
