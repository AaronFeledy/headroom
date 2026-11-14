package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type readinessMarker struct {
	Nonce      string `json:"nonce"`
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	Executable string `json:"executable"`
}

func matchesReadinessMarker(data []byte, expected readinessMarker) bool {
	var ready readinessMarker
	return len(data) <= 4096 && json.Unmarshal(data, &ready) == nil &&
		ready.PID > 0 && ready.PID == expected.PID && ready.Nonce == expected.Nonce &&
		ready.Version == expected.Version && filepath.IsAbs(ready.Executable) &&
		samePath(ready.Executable, expected.Executable)
}

func launchAndAwaitReady(request ApplyRequest, state InstallState, onLaunch func(int, string, string) error) error {
	inspection, err := inspectState(request.InstallRoot, state)
	if err != nil {
		return err
	}
	manifest, err := installedManifest(request.InstallRoot, state)
	if err != nil {
		return err
	}
	packagePath := manifest.Components.Application.Path
	if request.CandidateRole == RoleCLI {
		packagePath = PackageCLIPath(manifest.Platform, manifest.PackageKind)
	}
	if request.CandidateRole == RoleServer {
		packagePath = manifest.Components.Server.Path
	}
	executable := installedComponentPath(request.InstallRoot, state.VersionPath, packagePath)
	for _, missing := range inspection.Missing {
		if !isAuxiliaryPath(missing) || missing == strings.TrimPrefix(packagePath, "bundle/") {
			return errors.New("target app/runtime is incomplete")
		}
	}
	_ = os.Remove(request.ReadyPath)
	environment := authoritativeApplyEnvironment(os.Environ(), map[string]string{
		"HEADROOM_INSTALL_ROOT": request.InstallRoot, "HEADROOM_LAUNCHER_PATH": request.EntryPath,
		"HEADROOM_PACKAGE_VERSION": state.ActiveVersion, "HEADROOM_READY_NONCE": request.Nonce,
	})
	arguments := append([]string{executable}, request.RelaunchArguments...)
	arguments = append(arguments, "--headroom-update-restart", "--headroom-ready-file", request.ReadyPath)
	process, err := os.StartProcess(executable, arguments, &os.ProcAttr{Env: environment, Files: []*os.File{nil, nil, nil}})
	if err != nil {
		return err
	}
	token, tokenErr := captureProcessToken(process.Pid, executable)
	if tokenErr != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return tokenErr
	}
	done := make(chan error, 1)
	go func() { _, waitErr := process.Wait(); done <- waitErr }()
	if onLaunch != nil {
		if err = onLaunch(process.Pid, executable, token); err != nil {
			_ = process.Kill()
			<-done
			return err
		}
	}
	expected := readinessMarker{Nonce: request.Nonce, PID: process.Pid, Version: state.ActiveVersion, Executable: executable}
	deadline := time.Now().Add(time.Duration(request.ReadyTimeoutMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			return fmt.Errorf("replacement app exited before readiness: %v", waitErr)
		default:
		}
		data, readErr := readBoundedFile(request.ReadyPath, 4096)
		if readErr == nil && matchesReadinessMarker(data, expected) {
			select {
			case waitErr := <-done:
				return fmt.Errorf("replacement app exited during readiness: %v", waitErr)
			case <-time.After(200 * time.Millisecond):
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = process.Kill()
	<-done
	return errors.New("replacement app readiness timed out")
}
