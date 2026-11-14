package main

import (
	"context"
	"flag"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
)

func install(args []string) (any, error) {
	set, v, p, a, n := flags("install")
	archive := set.String("archive", "", "package archive")
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	entry := set.String("entry-path", defaultEntryPath(), "stable launcher entry")
	cliEntry := set.String("cli-entry-path", "", "stable public CLI entry")
	coordinate := set.Bool("coordinate", false, "coordinate replacement with the running desktop")
	if err := set.Parse(args); err != nil {
		return contract.InstallResult{}, err
	}
	return contract.InstallPackage(context.Background(), contract.InstallOptions{
		Archive: *archive, InstallRoot: *root, EntryPath: *entry, CLIEntryPath: *cliEntry,
		Expected: expectations(v, p, a, n), Coordinate: *coordinate,
	})
}

func activate(args []string) (any, error) {
	set := flag.NewFlagSet("activate", flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	versionPath := set.String("version-path", "", "exact installed generation")
	noLaunch := set.Bool("no-launch", false, "skip desktop launch")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	return contract.ActivateInstall(context.Background(), contract.ActivationOptions{InstallRoot: *root, VersionPath: *versionPath, NoLaunch: *noLaunch})
}
