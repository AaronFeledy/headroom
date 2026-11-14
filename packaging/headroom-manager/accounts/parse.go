package accounts

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

var errUnknownCommand = errors.New("unknown accounts command; use list, add, login, set, or remove")

func parse(o Options) (command, error) {
	var c command
	var enable, disable bool
	set := flag.NewFlagSet("accounts", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&c.config, "config", "", "server config path")
	set.StringVar(&c.label, "label", "", "account label")
	set.StringVar(&c.dir, "config-dir", "", "Claude Code config directory")
	set.BoolVar(&c.json, "json", false, "write accounts as JSON")
	set.BoolVar(&enable, "enable", false, "enable the account")
	set.BoolVar(&disable, "disable", false, "disable the account")
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
			return c, errors.New("usage: headroom accounts list [--json] [--config PATH]")
		}
	case "add", "login", "set", "remove":
		if len(positional) > 1 && positional[1] != "claude" {
			return c, errors.New("only the claude provider is supported")
		}
		if len(positional) != 3 {
			return c, fmt.Errorf("usage: headroom accounts %s claude <id> [options]", c.action)
		}
		c.id = positional[2]
	default:
		return c, errUnknownCommand
	}
	var flagErr error
	var hasEnable, hasDisable bool
	set.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "label":
			if c.action != "add" && c.action != "set" {
				flagErr = errors.New("--label is only supported by accounts add and set")
			}
			c.changes.Label = &c.label
		case "config-dir":
			if c.action != "add" {
				flagErr = errors.New("--config-dir is only supported by accounts add")
			}
		case "json":
			if c.action != "list" {
				flagErr = errors.New("--json is only supported by accounts list")
			}
		case "enable", "disable":
			if c.action != "set" {
				flagErr = errors.New("--enable and --disable are only supported by accounts set")
			}
			if f.Name == "enable" {
				hasEnable = true
				c.changes.Enabled = &enable
			} else {
				hasDisable = true
				enabled := !disable
				c.changes.Enabled = &enabled
			}
		}
	})
	if hasEnable && hasDisable {
		return c, errors.New("--enable and --disable cannot be combined")
	}
	return c, flagErr
}
