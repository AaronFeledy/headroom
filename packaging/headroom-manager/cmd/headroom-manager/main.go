package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/contract"
	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/internal/trayhost"
)

var buildVersion = "dev"

// invocationRole is set to "cli" only for bootstrap/headroom-cli. The legacy
// bootstrap/headroom build keeps its historical GUI behavior even when it is
// installed under the public headroom name during a migration.
var invocationRole = ""

type output struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() { os.Exit(run(os.Args)) }
func run(args []string) int {
	if len(args) == 2 && args[1] == "--headroom-tray-host" {
		if runtime.GOOS != "windows" || invocationRole == contract.RoleCLI {
			return 1
		}
		root, err := associatedInstallRoot()
		if err != nil {
			return 1
		}
		inspection := contract.InspectInstall(root)
		executable, err := os.Executable()
		if err != nil || !inspection.TrustedIdentity || !inspection.Complete || !strings.EqualFold(filepath.Clean(executable), filepath.Clean(inspection.LauncherPath)) {
			return 1
		}
		if err := trayhost.Run(os.Stdin, os.Stdout, executable, inspection.ActiveExecutable); err != nil {
			return 1
		}
		return 0
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(args[0])), ".exe")
	if invocationRole == contract.RoleCLI {
		arguments := args[1:]
		if name == "usage-server" {
			arguments = append([]string{"serve"}, arguments...)
		}
		if err := launchRole(contract.RoleCLI, arguments); err != nil {
			return reportCLILaunchFailure(err)
		}
		return 0
	}
	if name == "headroom" || name == "headroom-gui" || name == "headroom-launcher" || len(args) == 1 {
		if err := launchRole(contract.RoleApplication, args[1:]); err != nil {
			showLaunchError(err.Error())
			return emit("launch", nil, err)
		}
		return 0
	}
	command := args[1]
	var result any
	var err error
	switch command {
	case "asset-name":
		result, err = assetName(args[2:])
	case "inspect":
		result, err = inspect(args[2:])
	case "verify":
		result, err = verify(args[2:])
	case "stage":
		result, err = stage(args[2:])
	case "install":
		result, err = install(args[2:])
	case "migrate-entries":
		result, err = migrateEntries(args[2:])
	case "check-update":
		result, err = checkUpdate(args[2:])
	case "stage-update":
		result, err = stageUpdate(args[2:], false)
	case "stage-repair":
		result, err = stageUpdate(args[2:], true)
	case "prepare-apply":
		result, err = prepareApply(args[2:])
	case "apply":
		result, err = applyPrepared(args[2:])
	case "recover":
		result, err = recoverInstall(args[2:])
	case "create-package":
		result, err = createPackage(args[2:])
	case "create-release":
		result, err = createRelease(args[2:])
	case "materialize-links":
		result, err = materializeLinks(args[2:])
	default:
		err = fmt.Errorf("unknown command %q", command)
	}
	return emit(command, result, err)
}

func migrateEntries(args []string) (any, error) {
	set := flag.NewFlagSet("migrate-entries", flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	entry := set.String("entry-path", "", "stable application entry")
	cliEntry := set.String("cli-entry-path", "", "stable public CLI entry")
	replaceServer := set.Bool("replace-legacy-server", false, "replace a pre-existing legacy usage-server entry")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	return contract.MigrateManagedEntries(*root, *entry, *cliEntry, *replaceServer)
}

func prepareApply(args []string) (any, error) {
	set := flag.NewFlagSet("prepare-apply", flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	entry := set.String("entry-path", defaultEntryPath(), "stable launcher entry")
	record := set.String("stage-record", "", "verified stage record")
	pid := set.Int("current-pid", 0, "running Headroom process")
	executable := set.String("current-executable", "", "running Headroom executable")
	currentRole := set.String("current-role", contract.RoleApplication, "running Headroom component role")
	candidateRole := set.String("candidate-role", contract.RoleApplication, "replacement Headroom component role")
	childPID := set.Int("owned-child-pid", 0, "owned local server process")
	childExecutable := set.String("owned-child-executable", "", "owned local server executable")
	var relaunch listFlag
	var participants participantFlags
	set.Var(&relaunch, "relaunch-arg", "argument to preserve when restarting")
	set.Var(&participants, "participant", "additional process identity as compact JSON")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	manager, err := os.Executable()
	if err != nil {
		return nil, err
	}
	prepared, err := contract.PrepareApply(manager, contract.ApplyRequest{InstallRoot: *root, EntryPath: *entry, StageRecord: *record,
		CurrentPID: *pid, CurrentExecutable: *executable, CurrentRole: *currentRole, CandidateRole: *candidateRole,
		OwnedChildPID: *childPID, OwnedChildExecutable: *childExecutable, AdditionalProcesses: participants,
		RelaunchArguments: relaunch})
	if err != nil {
		return nil, err
	}
	if err = startApplyManager(prepared.ManagerPath, prepared.RequestPath); err != nil {
		return nil, err
	}
	if err = contract.WaitForApplyAcknowledgement(prepared, 10*time.Second); err != nil {
		return nil, err
	}
	return prepared, nil
}

func applyPrepared(args []string) (any, error) {
	set := flag.NewFlagSet("apply", flag.ContinueOnError)
	request := set.String("request", "", "private apply request")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	return contract.ApplyPrepared(*request)
}
func recoverInstall(args []string) (any, error) {
	set := flag.NewFlagSet("recover", flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	if err := contract.RecoverInstall(*root); err != nil {
		return nil, err
	}
	return map[string]bool{"recovered": true}, nil
}

func checkUpdate(args []string) (any, error) {
	set := flag.NewFlagSet("check-update", flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	cancelStdin := set.Bool("cancel-stdin", false, "cancel when standard input closes")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	ctx, cancel := cancellableContext(*cancelStdin)
	defer cancel()
	return contract.DefaultUpdateClient().Check(ctx, *root)
}

func stageUpdate(args []string, repair bool) (any, error) {
	command := "stage-update"
	if repair {
		command = "stage-repair"
	}
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	cancelStdin := set.Bool("cancel-stdin", false, "cancel when standard input closes")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	ctx, cancel := cancellableContext(*cancelStdin)
	defer cancel()
	if repair {
		return contract.DefaultUpdateClient().StageRepair(ctx, *root)
	}
	return contract.DefaultUpdateClient().StageLatest(ctx, *root)
}

func cancellableContext(enabled bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if enabled {
		go func() { var input [1]byte; _, _ = os.Stdin.Read(input[:]); cancel() }()
	}
	return ctx, cancel
}
func assetName(args []string) (any, error) {
	set := flag.NewFlagSet("asset-name", flag.ContinueOnError)
	version := set.String("version", "", "version")
	platform := set.String("platform", "", "platform")
	arch := set.String("arch", "", "architecture")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	asset, err := contract.AssetName(*version, *platform, *arch)
	if err != nil {
		return nil, err
	}
	root, err := contract.ArchiveRoot(*version, *platform, *arch)
	return map[string]string{"asset_name": asset, "archive_root": root}, err
}

// reportCLILaunchFailure keeps the public command's stdout reserved for usage
// output. A CLI that ran already reported its own diagnostics, so only its exit
// status is mirrored; a launcher failure is described on stderr.
func reportCLILaunchFailure(err error) int {
	var status exitStatusError
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "headroom: "+err.Error())
	return 2
}

func emit(command string, result any, err error) int {
	o := output{OK: err == nil, Command: command, Result: result}
	if err != nil {
		o.Error = err.Error()
	}
	data, _ := json.Marshal(o)
	fmt.Println(string(data))
	if err != nil {
		return 2
	}
	return 0
}
func flags(command string) (*flag.FlagSet, *string, *string, *string, *string) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	return set, set.String("version", "", "expected version"), set.String("platform", "", "expected platform"), set.String("arch", "", "expected architecture"), set.String("asset", "", "expected asset name")
}
func expectations(v, p, a, n *string) contract.Expectations {
	return contract.Expectations{Version: *v, Platform: *p, Architecture: *a, AssetName: *n}
}
func inspect(args []string) (any, error) {
	set := flag.NewFlagSet("inspect", flag.ContinueOnError)
	archive := set.String("archive", "", "package archive")
	root := set.String("install-root", "", "installation root")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	if *archive != "" && *root != "" {
		return nil, fmt.Errorf("inspect accepts either --archive or --install-root")
	}
	if *archive != "" {
		manifest, archiveRoot, err := contract.InspectArchive(*archive)
		if err != nil {
			return nil, err
		}
		return map[string]any{"manifest": manifest, "archive_root": archiveRoot}, nil
	}
	if *root == "" {
		*root = defaultInstallRoot()
	}
	return contract.InspectInstall(*root), nil
}
func verify(args []string) (any, error) {
	set, v, p, a, n := flags("verify")
	archive := set.String("archive", "", "package archive")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	m, root, err := contract.VerifyArchive(*archive, expectations(v, p, a, n))
	if err == nil {
		err = contract.CheckExpectations(m, expectations(v, p, a, n))
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"manifest": m, "archive_root": root}, nil
}
func stage(args []string) (any, error) {
	set, v, p, a, n := flags("stage")
	archive := set.String("archive", "", "package archive")
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	return contract.StageArchive(*archive, *root, expectations(v, p, a, n))
}
func install(args []string) (any, error) {
	set, v, p, a, n := flags("install")
	archive := set.String("archive", "", "package archive")
	root := set.String("install-root", defaultInstallRoot(), "installation root")
	entry := set.String("entry-path", defaultEntryPath(), "stable launcher entry")
	cliEntry := set.String("cli-entry-path", "", "stable public CLI entry")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	return contract.InstallArchiveWithCLIEntry(*archive, *root, *entry, *cliEntry, expectations(v, p, a, n))
}
func createPackage(args []string) (any, error) {
	set := flag.NewFlagSet("create-package", flag.ContinueOnError)
	root := set.String("root", "", "package root")
	output := set.String("output", "", "archive output")
	version := set.String("version", buildVersion, "version")
	platform := set.String("platform", runtime.GOOS, "platform")
	arch := set.String("arch", nativeArch(), "architecture")
	qt := set.String("qt-version", "6.8.3", "Qt version")
	baseline := set.String("baseline", "", "runtime baseline")
	kind := set.String("kind", "", "package kind: cli, or empty for desktop")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	if *kind == contract.PackageKindCLI {
		*qt = ""
	}
	manifest, err := contract.BuildManifestForKind(*kind, *root, *version, *platform, *arch, *qt, *baseline)
	if err != nil {
		return nil, err
	}
	if filepath.Base(*output) != manifest.AssetName {
		return nil, fmt.Errorf("archive output must be named %s", manifest.AssetName)
	}
	if err = contract.WriteJSON(filepath.Join(*root, contract.PackageManifestName), manifest); err != nil {
		return nil, err
	}
	if err = contract.WriteArchive(*root, *output); err != nil {
		return nil, err
	}
	if _, _, err = contract.VerifyArchive(*output, contract.Expectations{Version: *version, Platform: *platform, Architecture: *arch, AssetName: manifest.AssetName}); err != nil {
		return nil, err
	}
	size, hash, err := contract.FileDigest(*output)
	if err != nil {
		return nil, err
	}
	return map[string]any{"manifest": manifest, "archive_size": size, "archive_sha256": hash}, nil
}
func createRelease(args []string) (any, error) {
	set := flag.NewFlagSet("create-release", flag.ContinueOnError)
	output := set.String("output", "", "release manifest output")
	version := set.String("version", buildVersion, "version")
	legacy := set.Bool("legacy", false, "create the legacy Windows/Linux release manifest")
	kind := set.String("kind", "", "package kind: cli, or empty for desktop")
	var packages listFlag
	set.Var(&packages, "package", "package archive (repeatable)")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	wantName := "Headroom-v" + *version + "-release-all.json"
	wantCount := 5
	if *legacy {
		wantName, wantCount = "Headroom-v"+*version+"-release.json", 3
	}
	if *kind == contract.PackageKindCLI && !*legacy {
		wantName, wantCount = "Headroom-CLI-v"+*version+"-release.json", 6
	} else if *kind != "" {
		return nil, fmt.Errorf("unsupported package kind or legacy combination")
	}
	if filepath.Base(*output) != wantName {
		return nil, fmt.Errorf("release manifest output must be named %s", wantName)
	}
	release := contract.ReleaseManifest{PackageKind: *kind, Schema: contract.SchemaVersion, Product: "Headroom", Version: *version}
	for _, archive := range packages {
		manifest, root, err := contract.VerifyArchive(archive, contract.Expectations{})
		if err != nil {
			return nil, err
		}
		if manifest.Version != *version || manifest.PackageKind != *kind {
			return nil, fmt.Errorf("package version or kind mismatch")
		}
		size, hash, err := contract.FileDigest(archive)
		if err != nil {
			return nil, err
		}
		release.Packages = append(release.Packages, contract.ReleasePackage{Platform: manifest.Platform, Architecture: manifest.Architecture, AssetName: manifest.AssetName, Size: size, SHA256: hash, PackageManifestPath: root + "/" + contract.PackageManifestName, Components: manifest.Components})
	}
	sort.Slice(release.Packages, func(i, j int) bool {
		return release.Packages[i].Platform+"/"+release.Packages[i].Architecture < release.Packages[j].Platform+"/"+release.Packages[j].Architecture
	})
	if len(release.Packages) != wantCount {
		if *kind == contract.PackageKindCLI {
			return nil, fmt.Errorf("CLI release manifest requires all six Windows, Linux, and macOS targets")
		}
		if *legacy {
			return nil, fmt.Errorf("legacy release manifest requires Windows x64/ARM64 and Linux x86_64 packages")
		}
		return nil, fmt.Errorf("full release manifest requires Windows x64/ARM64, Linux x86_64, and macOS x86_64/ARM64 packages")
	}
	encoded, err := json.Marshal(release)
	if err != nil {
		return nil, err
	}
	if _, err = contract.DecodeReleaseManifest(strings.NewReader(string(encoded))); err != nil {
		return nil, err
	}
	if err := contract.WriteJSON(*output, release); err != nil {
		return nil, err
	}
	return release, nil
}

func materializeLinks(args []string) (any, error) {
	set := flag.NewFlagSet("materialize-links", flag.ContinueOnError)
	root := set.String("root", "", "package root")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	if err := contract.MaterializeLinks(*root); err != nil {
		return nil, err
	}
	return map[string]string{"root": *root}, nil
}

type listFlag []string

func (f *listFlag) String() string         { return strings.Join(*f, ",") }
func (f *listFlag) Set(value string) error { *f = append(*f, value); return nil }

type participantFlags []contract.ProcessClaim

func (f *participantFlags) String() string { return fmt.Sprintf("%v", []contract.ProcessClaim(*f)) }
func (f *participantFlags) Set(value string) error {
	var claim contract.ProcessClaim
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claim); err != nil {
		return fmt.Errorf("invalid participant: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid participant: extra JSON")
	}
	if claim.ProcessToken != "" {
		return errors.New("participant token is manager-owned")
	}
	*f = append(*f, claim)
	return nil
}
func nativeArch() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return runtime.GOARCH
}
func defaultInstallRoot() string {
	if value := os.Getenv("HEADROOM_INSTALL_ROOT"); value != "" {
		return value
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Headroom")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Headroom")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(data, "headroom")
}
func defaultEntryPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(defaultInstallRoot(), "headroom.exe")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(os.Getenv("HOME"), "Applications", "Headroom.app", "Contents", "MacOS", "headroom")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "bin", "headroom")
}
