// Package accounts implements guided setup for extra Claude accounts.
package accounts

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	Desktop        Desktop
}

const usage = `Usage: headroom accounts list [--config PATH] [--json]
       headroom accounts add claude <id> [--label LABEL] [--config-dir DIR] [--config PATH]
       headroom accounts login claude <id> [--config PATH]
       headroom accounts set claude <id> [--label LABEL] [--enable | --disable] [--config PATH]
       headroom accounts remove claude <id> [--config PATH]
`

type command struct {
	action, id, label, dir, config string
	json                           bool
	changes                        accountconfig.AccountChanges
}

func Run(ctx context.Context, options Options) (runErr error) {
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
	defer func() {
		var validation *accountconfig.ValidationError
		if errors.As(runErr, &validation) {
			runErr = fmt.Errorf("%w. Run headroom serve --check-config --config %s for details.", runErr, target.Path)
		}
	}()
	if err != nil {
		return err
	}
	switch c.action {
	case "list":
		return o.list(ctx, target, c.json)
	case "add":
		return o.add(ctx, c, selection{target: target, record: record})
	case "login":
		return o.loginAccount(ctx, c, target)
	case "set":
		change, err := accountconfig.Update(ctx, target, c.id, c.changes)
		if errors.Is(err, accountconfig.ErrNoChanges) {
			return fmt.Errorf("%w; pass --label, --enable, or --disable", err)
		}
		if err != nil {
			return knownAccountError(ctx, target, err)
		}
		outcome, err := o.restart(ctx, selection{target: target, record: record}, change)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(o.Stdout, "Updated Claude (%s) in %s.\n%s\n", change.Account.Label, change.Path, renderRestart(outcome, record, change.Path))
		return err
	case "remove":
		change, err := accountconfig.Remove(ctx, target, c.id)
		if err != nil {
			return knownAccountError(ctx, target, err)
		}
		outcome, err := o.restart(ctx, selection{target: target, record: record}, change)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(o.Stdout, "Removed Claude (%s) from %s. Its Claude Code sign-in in %s was not changed.\n%s\n", change.Account.Label, change.Path, change.Account.ConfigDir, renderRestart(outcome, record, change.Path))
		return err
	default:
		return errUnknownCommand
	}
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
