package accounts

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestValidationErrorsCarryConfigHintAndOriginalError(t *testing.T) {
	for _, args := range [][]string{
		{"list"}, {"list", "--json"}, {"add", "claude", "work"},
		{"login", "claude", "work"}, {"set", "claude", "work", "--disable"}, {"remove", "claude", "work"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			o, _, path := fixture(t)
			before := "poll_interval: sensitive-secret\n"
			write(t, path, before)
			o.Args = append(args, "--config", path)

			err := Run(context.Background(), o)

			var validation *accountconfig.ValidationError
			if !errors.As(err, &validation) || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "sensitive-secret") || contents(t, path) != before {
				t.Fatalf("error %v; config %q", err, contents(t, path))
			}
		})
	}
}

func TestLoginFailurePreservesConfigAndCause(t *testing.T) {
	o, out, path := fixture(t)
	savedAccount(t, o, path)
	before := contents(t, path)
	denied := errors.New("synthetic login rejected")
	o.Login = func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error { return denied }
	probes := 0
	o.SignIn = func(context.Context, string) accountconfig.SignIn {
		probes++
		return accountconfig.SignIn{State: "signed_in"}
	}
	o.Args = []string{"login", "claude", "work", "--config", path}

	err := Run(context.Background(), o)

	if !errors.Is(err, denied) || probes != 0 || out.Len() != 0 || contents(t, path) != before {
		t.Fatalf("error %v; probes %d; output %s; config %q", err, probes, out, contents(t, path))
	}
}

func TestFlagsAreRestrictedToTheirSubcommands(t *testing.T) {
	for _, args := range [][]string{
		{"login", "claude", "work", "--label", "Work"},
		{"remove", "claude", "work", "--json"},
		{"add", "claude", "work", "--enable"},
		{"list", "--disable"},
		{"set", "claude", "work", "--config-dir", "/synthetic"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			o, _, path := fixture(t)
			o.Args = append(args, "--config", path)

			if err := Run(context.Background(), o); err == nil {
				t.Fatal("accepted flags from another command")
			}

			if contents(t, path) != "" {
				t.Fatal("invalid command changed config")
			}
		})
	}
}
