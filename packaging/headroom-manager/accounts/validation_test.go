package accounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestValidationPrecedesLogin(t *testing.T) {
	const saved = "providers:\n  claude:\n    accounts:\n      - id: saved\n        label: Saved\n        config_dir: /synthetic/saved\n"
	var full strings.Builder
	full.WriteString("providers:\n  claude:\n    accounts:\n")
	for i := range 8 {
		fmt.Fprintf(&full, "      - id: saved%d\n        config_dir: /synthetic/saved%d\n", i, i)
	}
	for _, tc := range []struct {
		name, data, id string
		flags          []string
		want           error
	}{
		{"invalid id", "", "WORK", nil, accountconfig.ErrInvalidID},
		{"invalid label", "", "work", []string{"--label", "invalid\x01label"}, accountconfig.ErrInvalidLabel},
		{"invalid directory", "", "work", []string{"--config-dir", "relative/dir"}, accountconfig.ErrInvalidConfigDir},
		{"duplicate id", saved, "saved", nil, accountconfig.ErrDuplicateID},
		{"duplicate label", saved, "work", []string{"--label", "saved"}, accountconfig.ErrDuplicateLabel},
		{"duplicate directory", saved, "work", []string{"--config-dir", "/synthetic/saved"}, accountconfig.ErrDuplicateConfigDir},
		{"too many accounts", full.String(), "work", nil, accountconfig.ErrTooManyAccounts},
		{"invalid existing config", "poll_interval: sensitive-secret\n", "work", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, out, path := fixture(t)
			write(t, path, tc.data)
			logins, probes := 0, 0
			o.SignIn = func(context.Context, string) accountconfig.SignIn {
				probes++
				t.Fatal("probe before validation")
				return accountconfig.SignIn{}
			}
			o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
				logins++
				return nil
			}
			o.Args = append([]string{"add", "claude", tc.id, "--config", path}, tc.flags...)
			err := Run(context.Background(), o)
			if err == nil {
				t.Fatal("expected validation failure")
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
			} else {
				var validation *accountconfig.ValidationError
				if !errors.As(err, &validation) || strings.Contains(err.Error(), "sensitive-secret") {
					t.Fatalf("existing config error was not redacted: %v", err)
				}
			}
			if logins != 0 || probes != 0 {
				t.Fatalf("login %d probes %d before validation", logins, probes)
			}
			if contents(t, path) != tc.data || out.Len() != 0 {
				t.Fatalf("config or output changed: %s", out)
			}
		})
	}
}
