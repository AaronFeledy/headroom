package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAccountsDispatchUnavailableAndCallback(t *testing.T) {
	options, _, _ := baseOptions(fixtureUsage(t))
	options.Args = []string{"accounts", "list"}
	err := Run(context.Background(), options)
	if err == nil || err.Error() != "accounts is unavailable in this Headroom build" {
		t.Fatalf("unavailable error = %v", err)
	}

	called := false
	var got []string
	options, _, _ = baseOptions(fixtureUsage(t))
	options.Args = []string{"accounts", "add", "claude", "work", "--label", "Work"}
	options.Accounts = func(_ context.Context, args []string) error {
		called = true
		got = append([]string(nil), args...)
		return nil
	}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !called || !reflect.DeepEqual(got, []string{"add", "claude", "work", "--label", "Work"}) {
		t.Fatalf("Accounts callback args = %#v, called = %v", got, called)
	}

	options, _, stderr := baseOptions(fixtureUsage(t))
	options.Args = []string{"--help"}
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	help := stderr.String()
	for _, form := range []string{
		"headroom accounts list [--config PATH]",
		"headroom accounts add claude <id> [--label LABEL] [--config-dir DIR] [--config PATH]",
		"headroom accounts remove claude <id> [--config PATH]",
	} {
		if !strings.Contains(help, form) {
			t.Errorf("help omitted %q:\n%s", form, help)
		}
	}
}

func TestAccountsCallbackErrorPropagates(t *testing.T) {
	options, _, _ := baseOptions(fixtureUsage(t))
	options.Args = []string{"accounts"}
	want := errors.New("fixture accounts failure")
	options.Accounts = func(context.Context, []string) error { return want }
	if err := Run(context.Background(), options); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
