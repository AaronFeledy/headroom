// Package accounts implements guided setup for extra Claude accounts.
package accounts

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

type Service interface {
	Read() (contract.ManagedServiceRecord, error)
	Restart(context.Context, contract.ManagedServiceRecord, func() error) (contract.ManagedServiceRecord, error)
}

type Options struct {
	Args, Env      []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getwd          func() (string, error)
	Login          func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error
	SignIn         func(context.Context, string) accountconfig.SignIn
	Service        Service
}

const usage = `Usage: headroom accounts list [--config PATH]
       headroom accounts add claude <id> [--label LABEL] [--config-dir DIR] [--config PATH]
       headroom accounts remove claude <id> [--config PATH]
`

type command struct {
	action, id, label, dir, config string
}

func Run(ctx context.Context, options Options) error {
	o := options.defaults()
	c, err := parse(o)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(o.Stdout, usage)
		return err
	}
	if err != nil {
		return err
	}
	target, record, err := o.target(c.config)
	if err != nil {
		return err
	}
	switch c.action {
	case "list":
		return o.list(ctx, target)
	case "add":
		return o.add(ctx, c, selection{target: target, record: record})
	case "remove":
		change, err := accountconfig.Remove(ctx, target, c.id)
		if errors.Is(err, accountconfig.ErrAccountNotFound) {
			known, listErr := accountconfig.List(ctx, target)
			if listErr != nil {
				return listErr
			}
			ids := make([]string, 0, len(known))
			for _, a := range known {
				ids = append(ids, a.ID)
			}
			return fmt.Errorf("%w; known ids: %v", err, ids)
		}
		if err != nil {
			return err
		}
		outcome, err := o.restart(ctx, selection{target: target, record: record}, change)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(o.Stdout, "Removed Claude (%s) from %s. Its Claude Code sign-in in %s was not changed.\n%s\n", change.Account.Label, change.Path, change.Account.ConfigDir, renderRestart(outcome, record, change.Path))
		return err
	default:
		return errors.New("unknown accounts command; use list, add, or remove")
	}
}

func parse(o Options) (command, error) {
	var c command
	set := flag.NewFlagSet("accounts", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&c.config, "config", "", "server config path")
	set.StringVar(&c.label, "label", "", "account label")
	set.StringVar(&c.dir, "config-dir", "", "Claude Code config directory")
	set.Usage = func() {}
	var positional []string
	for args := o.Args; len(args) > 0; {
		if err := set.Parse(args); err != nil {
			return c, err
		}
		args = set.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}
	if len(positional) == 0 {
		return c, flag.ErrHelp
	}
	c.action = positional[0]
	switch c.action {
	case "list":
		if len(positional) != 1 {
			return c, errors.New("usage: headroom accounts list [--config PATH]")
		}
	case "add", "remove":
		if len(positional) > 1 && positional[1] != "claude" {
			return c, errors.New("only the claude provider is supported")
		}
		if len(positional) != 3 {
			return c, fmt.Errorf("usage: headroom accounts %s claude <id> [options]", c.action)
		}
		c.id = positional[2]
	default:
		return c, errors.New("unknown accounts command; use list, add, or remove")
	}
	if c.action != "add" {
		var addFlag bool
		set.Visit(func(f *flag.Flag) {
			if f.Name == "label" || f.Name == "config-dir" {
				addFlag = true
			}
		})
		if addFlag {
			return c, errors.New("--label and --config-dir are only supported by accounts add")
		}
	}
	return c, nil
}

func (o Options) defaults() Options {
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Getwd == nil {
		o.Getwd = os.Getwd
	}
	if o.Login == nil {
		o.Login = login
	}
	if o.SignIn == nil {
		o.SignIn = accountconfig.ClaudeSignIn
	}
	return o
}

func (o Options) list(ctx context.Context, target accountconfig.Target) error {
	items, err := accountconfig.List(ctx, target)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(o.Stdout, "Config: %s\n", target.Path); err != nil {
		return err
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(o.Stdout, "No extra Claude accounts. Add one with: headroom accounts add claude <id>")
		return err
	}
	w := tabwriter.NewWriter(o.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tLABEL\tSTATUS\tCONFIG DIR"); err != nil {
		return err
	}
	for _, a := range items {
		state := o.SignIn(ctx, a.ConfigDir).State
		status := signInState(state)
		if state == "locked" {
			status = "keychain locked"
		}
		if !a.Enabled {
			status = "disabled (" + status + ")"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Label, status, a.ConfigDir); err != nil {
			return err
		}
	}
	return w.Flush()
}
