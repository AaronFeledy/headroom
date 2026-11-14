package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/cli"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/pairing"
	serverapp "github.com/AaronFeledy/claude-usage-widget/server/app"
)

var version = "dev"

func main() {
	if handled, err := updateReadiness(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "headroom:", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, stopLauncher, launcherErr := launcherContext(ctx)
	if launcherErr != nil {
		fmt.Fprintln(os.Stderr, "headroom:", launcherErr)
		os.Exit(1)
	}
	defer stopLauncher()
	if len(os.Args) == 2 && os.Args[1] == pairing.RPCArgument {
		if err := pairRPC(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "headroom:", err)
			os.Exit(1)
		}
		return
	}
	err := cli.Run(ctx, cli.Options{
		Args: os.Args[1:], Env: os.Environ(), Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Version: version, Serve: managedServe, Update: coordinatedUpdate, Desktop: desktopCommand, Pair: pairCommand, Accounts: accountsCommand, FetchLocal: fetchLocalDesktop,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "headroom:", err)
		os.Exit(1)
	}
}

func managedServe(ctx context.Context, args, env []string, logger *slog.Logger, version string, stdin io.Reader, stdout io.Writer) error {
	// A help-looking flag value (for example a config filename) must not
	// bypass ownership registration for a server that will actually run.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		return serverapp.Run(ctx, args, env, logger, version, stdin, stdout)
	}
	if token := os.Getenv("HEADROOM_MANAGED_SERVICE_RESTART"); token != "" {
		record, err := contract.AcceptManagedServiceRestart(os.Getenv("HEADROOM_INSTALL_ROOT"), token, args)
		if err != nil {
			return err
		}
		if record.Supervisor == "" {
			defer contract.UnregisterManagedService(record)
		}
		return serverapp.RunWithReady(ctx, args, env, logger, version, stdin, stdout, func(ready serverapp.Ready) error {
			record.ConfigPath = ready.ConfigPath
			return contract.MarkManagedServiceReady(record)
		})
	}
	for _, argument := range args {
		if argument == "--ssh-stdio" {
			return serverapp.Run(ctx, args, env, logger, version, stdin, stdout)
		}
	}
	root := os.Getenv("HEADROOM_INSTALL_ROOT")
	if root == "" {
		return serverapp.Run(ctx, args, env, logger, version, stdin, stdout)
	}
	var record contract.ManagedServiceRecord
	var err error
	if systemdInvocation(env) {
		record, err = contract.AcceptSupervisedManagedServiceRestart(root, args)
		if errors.Is(err, os.ErrNotExist) {
			record, err = contract.RegisterManagedService(root, args)
		}
	} else {
		record, err = contract.RegisterManagedService(root, args)
	}
	if err != nil {
		return err
	}
	// A supervised stop is synchronous while the updater holds its install
	// lock. Leave that exact receipt for the updater/new unit to replace rather
	// than blocking shutdown trying to reacquire the same lock.
	if record.Supervisor == "" {
		defer contract.UnregisterManagedService(record)
	}
	return serverapp.RunWithReady(ctx, args, env, logger, version, stdin, stdout, func(ready serverapp.Ready) error {
		record.ConfigPath = ready.ConfigPath
		return contract.MarkManagedServiceReady(record)
	})
}

func systemdInvocation(environment []string) bool {
	for _, item := range environment {
		if strings.HasPrefix(item, "INVOCATION_ID=") || strings.HasPrefix(item, "SYSTEMD_EXEC_PID=") {
			return true
		}
	}
	return false
}

func updateReadiness(args []string) (bool, error) {
	if len(args) != 3 || args[0] != "--headroom-update-restart" || args[1] != "--headroom-ready-file" {
		return false, nil
	}
	nonce := os.Getenv("HEADROOM_READY_NONCE")
	root := os.Getenv("HEADROOM_INSTALL_ROOT")
	version := os.Getenv("HEADROOM_PACKAGE_VERSION")
	executable, err := os.Executable()
	if err != nil {
		return true, err
	}
	payload, err := json.Marshal(map[string]any{"nonce": nonce, "pid": os.Getpid(), "version": version, "executable": executable})
	if err != nil {
		return true, err
	}
	if err = writePrivateReady(args[2], append(payload, '\n'), root); err != nil {
		return true, err
	}
	time.Sleep(500 * time.Millisecond)
	return true, nil
}
