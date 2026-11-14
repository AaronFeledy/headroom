package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func (o Options) loginAccount(ctx context.Context, c command, target accountconfig.Target) error {
	items, err := accountconfig.List(ctx, target)
	if err != nil {
		return err
	}
	for _, a := range items {
		if a.ID != c.id {
			continue
		}
		if err := o.loginAndProbe(ctx, a.ConfigDir); err != nil {
			return err
		}
		_, err := fmt.Fprintf(o.Stdout, "Signed in Claude (%s). The server reads the new sign-in on its next poll.\n", a.Label)
		return err
	}
	return knownAccountError(ctx, target, accountconfig.ErrAccountNotFound)
}

func (o Options) loginAndProbe(ctx context.Context, dir string) error {
	if err := o.Login(ctx, dir, o.Stdin, o.Stdout, o.Stderr, loginEnvironment(o.Env, dir)); err != nil {
		if errors.Is(err, errClaudeNotFound) {
			return err
		}
		return fmt.Errorf("claude auth login failed: %w", err)
	}
	probe := o.SignIn(ctx, dir)
	if probe.State != "signed_in" {
		checked := make([]string, 0, len(probe.Checked))
		for _, source := range probe.Checked {
			checked = append(checked, fmt.Sprintf("%s (%s)", signInWhere(dir, source.Kind), signInState(source.Status)))
		}
		return fmt.Errorf("Claude Code did not save a sign-in Headroom can read for %s; checked: %s", dir, strings.Join(checked, ", "))
	}
	return nil
}

func knownAccountError(ctx context.Context, target accountconfig.Target, err error) error {
	if !errors.Is(err, accountconfig.ErrAccountNotFound) {
		return err
	}
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
