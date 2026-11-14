package main

import (
	"context"
	"os"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/accounts"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

func accountsCommand(ctx context.Context, args []string) error {
	return accounts.Run(ctx, accounts.Options{
		Args:    args,
		Env:     os.Environ(),
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Service: managedAccountsService{},
		Desktop: accountsDesktop{},
	})
}

type managedAccountsService struct{}

func (managedAccountsService) Read() (contract.ManagedServiceRecord, error) {
	root := os.Getenv("HEADROOM_INSTALL_ROOT")
	if root == "" {
		return contract.ManagedServiceRecord{}, os.ErrNotExist
	}
	return contract.ReadManagedService(root)
}

func (managedAccountsService) Restart(ctx context.Context, record contract.ManagedServiceRecord, rollback func() error) (contract.ManagedServiceRecord, error) {
	return contract.RestartSupervisedManagedService(ctx, record.InstallRoot, record, rollback)
}
