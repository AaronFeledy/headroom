package accountconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

// ResolvePath returns the path the server would load, including legacy migration.
func ResolvePath(args, env []string) (string, error) {
	path, err := config.ResolvePath(config.LoadOptions{Args: args, Env: env})
	return path, validationError(err)
}

func (t Target) options() config.LoadOptions {
	args := append([]string(nil), t.Args...)
	if t.Path != "" {
		args = append(args, "--config", t.Path)
	}
	return config.LoadOptions{Args: args, Env: t.Env, Dir: t.Dir}
}

func (t Target) resolve() (Target, error) {
	path, err := config.ResolvePath(t.options())
	if err != nil {
		return Target{}, validationError(err)
	}
	return Target{Path: path, Args: t.Args, Env: t.Env, Dir: t.Dir}, nil
}

func read(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// check returns the effective accounts, or the server's unwrapped validation error.
func check(data []byte, target Target) ([]Account, error) {
	cfg, err := config.Check(data, target.options())
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(cfg.Providers["claude"].Accounts))
	for _, a := range cfg.Providers["claude"].Accounts {
		accounts = append(accounts, Account{ID: a.ID, Label: a.Label, ConfigDir: a.ConfigDir, Enabled: a.Enabled})
	}
	return accounts, nil
}

func validationError(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{cause: err}
}

var accountRules = []struct{ server, public error }{
	{config.ErrInvalidAccountID, ErrInvalidID},
	{config.ErrInvalidAccountLabel, ErrInvalidLabel},
	{config.ErrInvalidAccountDir, ErrInvalidConfigDir},
	{config.ErrDuplicateAccountID, ErrDuplicateID},
	{config.ErrDuplicateAccountLabel, ErrDuplicateLabel},
	{config.ErrDuplicateAccountDir, ErrDuplicateConfigDir},
	{config.ErrTooManyAccounts, ErrTooManyAccounts},
}

// requestedAccountError names the rule the requested account broke. It is used
// only after the current file has validated, so these errors come from the
// caller's input rather than from existing config contents.
func requestedAccountError(err error) error {
	for _, rule := range accountRules {
		if errors.Is(err, rule.server) {
			return rule.public
		}
	}
	return validationError(err)
}

// List returns the extra Claude accounts in the current effective configuration.
func List(ctx context.Context, target Target) ([]Account, error) {
	target, err := target.resolve()
	if err != nil {
		return nil, err
	}
	data, err := read(ctx, target.Path)
	if err != nil {
		return nil, err
	}
	accounts, err := check(data, target)
	return accounts, validationError(err)
}

// DefaultClaudeConfigDir returns the home directory plus .claude-<id>, without cleaning.
// CheckAdd validates the account ID before the directory is used for login.
func DefaultClaudeConfigDir(id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + string(os.PathSeparator) + ".claude-" + id, nil
}

// ExpandConfigDir expands only a leading ~, ~/, or ~\ and requires an absolute result.
// Interior segments, separators, Unicode, and trailing slashes are preserved.
func ExpandConfigDir(dir string) (string, error) {
	expanded := dir
	if dir == "~" || len(dir) >= 2 && dir[0] == '~' && (dir[1] == '/' || dir[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		expanded = home + dir[1:]
	}
	if !filepath.IsAbs(expanded) {
		return "", ErrInvalidConfigDir
	}
	return expanded, nil
}
