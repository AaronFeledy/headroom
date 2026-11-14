package accounts

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestListHumanReadableSignInStates(t *testing.T) {
	for _, tc := range []struct{ state, token string }{
		{"signed_in", "signed in"},
		{"signed_out", "signed out"},
		{"expired", "expired"},
		{"locked", "keychain locked"},
		{"unreadable", "unreadable"},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/enabled=%t", tc.state, enabled), func(t *testing.T) {
				o, out, path := fixture(t)
				write(t, path, fmt.Sprintf("providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        config_dir: /synthetic/work\n        enabled: %t\n", enabled))
				o.SignIn = func(context.Context, string) accountconfig.SignIn {
					return accountconfig.SignIn{State: tc.state}
				}
				o.Args = []string{"list", "--config", path}
				if err := Run(context.Background(), o); err != nil {
					t.Fatal(err)
				}
				var row string
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.Contains(line, "/synthetic/work") {
						row = line
						break
					}
				}
				for _, want := range []string{"work", "Work", tc.token, "/synthetic/work"} {
					if !strings.Contains(row, want) {
						t.Fatalf("missing %q in row %q", want, row)
					}
				}
				if strings.Contains(row, "disabled") == enabled {
					t.Fatalf("enabled %t; row %q", enabled, row)
				}
			})
		}
	}
}
