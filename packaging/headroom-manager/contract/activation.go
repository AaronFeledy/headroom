package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type ActivationOptions struct {
	InstallRoot string
	VersionPath string
	NoLaunch    bool
}

func ActivateInstall(ctx context.Context, options ActivationOptions) (ActivationResult, error) {
	result := ActivationResult{Status: "unconfirmed", Reason: "readiness_unconfirmed", VersionPath: options.VersionPath}
	root, err := ValidateInstallRoot(options.InstallRoot)
	if err != nil {
		return result, err
	}
	lock, err := acquireInstallLock(root, activationTimeout)
	if err != nil {
		return result, err
	}
	state, stateErr := readTrustedState(root, false)
	closeErr := lock.Close()
	if stateErr != nil || closeErr != nil {
		return result, errors.Join(stateErr, closeErr)
	}
	if state == nil || !validVersionPath(*state) || state.VersionPath != options.VersionPath {
		return result, errors.New("activation generation is not the selected installed generation")
	}
	platform, arch, err := NativeTarget()
	if err != nil {
		return result, err
	}
	if state.Platform != platform || state.Architecture != arch {
		return result, errors.New("activation target is not native")
	}
	if options.NoLaunch || state.PackageKind == PackageKindCLI {
		result.Status, result.Reason = "skipped", "launch_disabled"
		if state.PackageKind == PackageKindCLI {
			result.Reason = "cli_only"
		}
		return result, nil
	}
	executable, inspection, err := ActiveExecutable(root)
	if err != nil {
		return result, err
	}
	info, absent, probeErr := probeDesktop(ctx, executable, root)
	if probeErr != nil {
		result.Reason = "desktop_unavailable"
		return result, nil
	}
	if !absent {
		return activationFromPrimary(root, *state, info)
	}
	manifest, err := installedManifest(root, *state)
	if err != nil {
		return result, err
	}
	ext := ""
	if state.Platform == "windows" {
		ext = ".exe"
	}
	if err = verifyStableEntry(root, inspection.LauncherPath, manifest, "bootstrap/headroom"+ext); err != nil {
		return result, err
	}
	directory, err := ensureOwnedDirectory(root, "activation", 0o700)
	if err != nil {
		return result, err
	}
	directory, err = os.MkdirTemp(directory, "launch-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(directory)
	nonce, err := randomHex(24)
	if err != nil {
		return result, err
	}
	readyPath := filepath.Join(directory, "ready.json")
	environment := authoritativeApplyEnvironment(os.Environ(), map[string]string{
		"HEADROOM_INSTALL_ROOT": root, "HEADROOM_READY_NONCE": nonce,
	})
	process, err := os.StartProcess(inspection.LauncherPath, []string{inspection.LauncherPath, "--headroom-ready-file", readyPath},
		&os.ProcAttr{Env: environment, Files: []*os.File{nil, nil, nil}})
	if err != nil {
		result.Reason = "launch_failed"
		return result, nil
	}
	go func() { _, _ = process.Wait() }()
	observation, cancel := context.WithTimeout(ctx, activationTimeout)
	defer cancel()
	expected := readinessMarker{Nonce: nonce, Version: state.ActiveVersion, Executable: executable}
	if (readinessObservation{path: readyPath, expected: expected}).wait(observation) == nil {
		return activationSelectedState(root, *state, ActivationResult{Status: "ready", Reason: "readiness_confirmed", VersionPath: state.VersionPath})
	}
	if ctx.Err() == nil {
		info, absent, probeErr = probeDesktop(ctx, executable, root)
		if probeErr == nil && !absent && !samePath(info.Executable, executable) {
			return activationFromPrimary(root, *state, info)
		}
	}
	return result, nil
}

func activationFromPrimary(root string, state InstallState, info desktopInfo) (ActivationResult, error) {
	result := ActivationResult{Status: "unconfirmed", Reason: "primary_not_ready", VersionPath: state.VersionPath}
	if *info.Ready {
		manifest, err := installedManifest(root, state)
		if err != nil {
			return result, err
		}
		if samePath(info.Executable, installedComponentPath(root, state.VersionPath, manifest.Components.Application.Path)) {
			result.Status, result.Reason = "ready", "primary_ready"
		} else {
			result.Status, result.Reason = "restart_required", "other_generation_running"
		}
	}
	return activationSelectedState(root, state, result)
}

func activationSelectedState(root string, expected InstallState, result ActivationResult) (ActivationResult, error) {
	lock, err := acquireInstallLock(root, activationTimeout)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	current, err := readTrustedState(root, false)
	if err != nil {
		return result, err
	}
	if current == nil || !statesEqual(*current, expected) {
		result.Status, result.Reason = "unconfirmed", "selected_generation_changed"
	}
	return result, nil
}
