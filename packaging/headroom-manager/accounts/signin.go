package accounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

var errClaudeNotFound = errors.New("Claude Code (claude) was not found on PATH; install Claude Code and run this command again")

func (o Options) add(ctx context.Context, c command, selected selection) error {
	dir := c.dir
	var err error
	if dir == "" {
		dir, err = accountconfig.DefaultClaudeConfigDir(c.id)
	} else {
		dir, err = accountconfig.ExpandConfigDir(dir)
	}
	if err != nil {
		return err
	}
	a := accountconfig.Account{ID: c.id, Label: c.label, ConfigDir: dir, Enabled: true}
	if err := accountconfig.CheckAdd(ctx, selected.target, a); err != nil {
		return err
	}
	probe := o.SignIn(ctx, dir)
	switch probe.State {
	case "signed_in":
		if _, err := fmt.Fprintf(o.Stdout, "Found an existing Claude Code sign-in in %s; skipping login.\n", signInWhere(dir, probe.SourceKind)); err != nil {
			return err
		}
	case "signed_out", "expired":
		if err := o.Login(ctx, dir, o.Stdin, o.Stdout, o.Stderr, loginEnvironment(o.Env, dir)); err != nil {
			if errors.Is(err, errClaudeNotFound) {
				return err
			}
			return fmt.Errorf("claude auth login failed: %w", err)
		}
		probe = o.SignIn(ctx, dir)
		if probe.State != "signed_in" {
			checked := make([]string, 0, len(probe.Checked))
			for _, source := range probe.Checked {
				checked = append(checked, fmt.Sprintf("%s (%s)", signInWhere(dir, source.Kind), signInState(source.Status)))
			}
			return fmt.Errorf("Claude Code did not save a sign-in Headroom can read for %s; checked: %s", dir, strings.Join(checked, ", "))
		}
	case "locked":
		return errors.New("the login keychain is locked; unlock it and run this command again")
	case "unreadable":
		return fmt.Errorf("cannot read the Claude Code sign-in in %s", signInWhere(dir, probe.SourceKind))
	default:
		return errors.New("unrecognized Claude sign-in state")
	}
	change, err := accountconfig.Add(ctx, selected.target, a)
	if err != nil {
		return err
	}
	outcome, err := o.restart(ctx, selected, change)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.Stdout, "Added Claude (%s) to %s.\n%s\n", change.Account.Label, change.Path, renderRestart(outcome, selected.record, change.Path))
	return err
}

func signInWhere(dir, kind string) string {
	switch kind {
	case "keychain":
		return "the macOS Keychain"
	case "cli":
		return filepath.Join(dir, ".credentials.json")
	default:
		return kind
	}
}

func signInState(state string) string {
	switch state {
	case "signed_in":
		return "signed in"
	case "signed_out":
		return "signed out"
	default:
		return state
	}
}

func loginEnvironment(env []string, dir string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "CLAUDE_CONFIG_DIR" || runtime.GOOS == "windows" && strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "CLAUDE_CONFIG_DIR="+dir)
}

func login(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, env []string) error {
	program, err := exec.LookPath("claude")
	if err != nil {
		return errClaudeNotFound
	}
	cmd := exec.CommandContext(ctx, program, "auth", "login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = env
	return cmd.Run()
}
