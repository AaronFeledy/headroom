package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

type selection struct {
	target accountconfig.Target
	record *contract.ManagedServiceRecord
}

func (o Options) target(explicit string) (accountconfig.Target, *contract.ManagedServiceRecord, error) {
	var record *contract.ManagedServiceRecord
	var receiptErr error
	if o.Service != nil {
		value, err := o.Service.Read()
		receiptErr = err
		if err == nil {
			record = &value
		}
	}
	if explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			cwd, err := o.Getwd()
			if err != nil {
				return accountconfig.Target{}, nil, fmt.Errorf("resolve --config: %w", err)
			}
			path = filepath.Join(cwd, path)
		}
		target := accountconfig.Target{Path: filepath.Clean(path), Env: o.Env}
		if record != nil && record.Ready && sameFile(record.ConfigPath, target.Path) {
			target.Args, target.Env = record.Arguments, record.Environment
			target.Dir = record.WorkingDirectory
		}
		return target, record, nil
	}
	if receiptErr != nil && !errors.Is(receiptErr, os.ErrNotExist) {
		return accountconfig.Target{}, nil, errors.New("cannot verify the managed server; specify --config PATH")
	}
	if record != nil {
		if !record.Ready || record.ConfigPath == "" {
			return accountconfig.Target{}, nil, errors.New("managed server config path is unavailable; specify --config PATH")
		}
		return accountconfig.Target{Path: record.ConfigPath, Args: record.Arguments, Env: record.Environment, Dir: record.WorkingDirectory}, record, nil
	}
	path, err := accountconfig.ResolvePath(nil, o.Env)
	return accountconfig.Target{Path: path, Env: o.Env}, nil, err
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	left, right := filepath.Clean(a), filepath.Clean(b)
	if left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right) {
		return true
	}
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	return err == nil && os.SameFile(first, second)
}

func (o Options) restart(ctx context.Context, selected selection, change accountconfig.Change) (restartOutcomeKind, error) {
	r := selected.record
	if r == nil {
		return manualRestart, nil
	}
	if !sameFile(r.ConfigPath, change.Path) {
		return otherConfig, nil
	}
	if !r.Ready || r.Supervisor != "systemd-user" {
		return foregroundServe, nil
	}
	rollbackInvoked := false
	var restoreErr error
	_, err := o.Service.Restart(ctx, *r, func() error {
		rollbackInvoked = true
		target := selected.target
		target.Path = change.Path
		restoreErr = accountconfig.Restore(context.WithoutCancel(ctx), target, change.Before, change.After)
		return restoreErr
	})
	switch {
	case err == nil:
		return restarted, nil
	case errors.Is(err, contract.ErrRestartRolledBack):
		return 0, &restartError{kind: rolledBack, Path: change.Path, cause: err}
	case rollbackInvoked && restoreErr != nil:
		return 0, &restartError{kind: notUndone, Path: change.Path, cause: restoreErr}
	case rollbackInvoked:
		return 0, &restartError{kind: undoneNotRestarted, Path: change.Path, cause: err}
	default:
		return 0, &restartError{kind: notRestarted, Path: change.Path, cause: err}
	}
}
