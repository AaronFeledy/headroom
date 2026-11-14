package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type ActivationResult struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	VersionPath string `json:"version_path"`
}

type InstallResult struct {
	State      *InstallState     `json:"state"`
	Activation *ActivationResult `json:"activation"`
}

type InstallOptions struct {
	Archive      string
	InstallRoot  string
	EntryPath    string
	CLIEntryPath string
	Expected     Expectations
	Coordinate   bool
}

type installTargets struct{ root, entry, cliEntry string }

func installStateDigest(root string) (string, error) {
	digest, err := digestFile(filepath.Join(root, StateName))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return digest, err
}

// InstallPackage never launches a direct installation. Platform integration must
// finish before the caller invokes ActivateInstall with the returned generation.
func InstallPackage(ctx context.Context, options InstallOptions) (result InstallResult, returned error) {
	root, err := ValidateInstallRoot(options.InstallRoot)
	if err != nil {
		return result, err
	}
	stage, err := StageArchive(options.Archive, root, options.Expected)
	if err != nil {
		return result, err
	}
	owned := true
	defer func() {
		if owned {
			returned = errors.Join(returned, os.RemoveAll(filepath.Dir(filepath.Dir(stage.PackageRoot))))
		}
	}()
	targets := installTargets{root, options.EntryPath, options.CLIEntryPath}
	var baseline *string
	if options.Coordinate && stage.PackageKind != PackageKindCLI {
		digest, digestErr := installStateDigest(root)
		if digestErr != nil {
			return result, digestErr
		}
		baseline = &digest
		prior, priorErr := readTrustedState(root, false)
		if priorErr != nil {
			return result, priorErr
		}
		result.State = prior
		bridge, _, bridgeErr := ActiveExecutable(root)
		if bridgeErr != nil {
			_, manifest, loadErr := LoadVerifiedStage(root, stageRecordPath(stage))
			if loadErr != nil {
				return result, loadErr
			}
			bridge = filepath.Join(stage.PackageRoot, filepath.FromSlash(manifest.Components.Application.Path))
		}
		info, absent, probeErr := probeDesktop(ctx, bridge, root)
		if probeErr != nil {
			return result, probeErr
		}
		if !absent {
			if *info.Busy || !*info.Ready {
				return result, errors.New("desktop is busy or not ready for reinstall")
			}
			if prior == nil {
				return result, errors.New("running desktop has no installed rollback state")
			}
			if bridgeErr == nil {
				if !samePath(info.Executable, bridge) {
					return result, errors.New("running desktop is not the installed rollback baseline")
				}
				if !samePath(targets.entry, prior.ApplicationEntryPath) || !samePath(targets.cliEntry, prior.CLIEntryPath) || (targets.cliEntry == "") != (prior.CLIEntryPath == "") {
					return result, errors.New("coordinated reinstall cannot change managed entry paths")
				}
				if err = ctx.Err(); err != nil {
					return result, err
				}
				// From this point even a missing reply can mean commit happened.
				owned = false
				return coordinateReinstall(ctx, bridge, reinstallObservation{root: root, prior: *prior, digest: digest, stage: stage})
			}
			// A verified staged bridge may establish the damaged baseline's primary.
			// Direct repair leaves that primary running; activation reports its identity.
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	state, err := installVerifiedStage(stage, targets, baseline)
	if err != nil {
		if selected, stateErr := readTrustedState(root, false); stateErr == nil {
			result.State = selected
		}
		return result, err
	}
	result.State = &state
	if state.PackageKind == PackageKindCLI {
		result.Activation = &ActivationResult{Status: "skipped", Reason: "cli_only", VersionPath: state.VersionPath}
	}
	return result, nil
}

func stageRecordPath(stage StageResult) string {
	return filepath.Join(filepath.Dir(filepath.Dir(stage.PackageRoot)), "verified-stage.json")
}

const activationTimeout = 10 * time.Second
