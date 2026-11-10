package contract

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

func priorAppReady(journal ApplyJournal, directory string) bool {
	manifest, err := installedManifest(journal.Request.InstallRoot, *journal.Previous)
	if err != nil {
		return false
	}
	component := manifest.Components.Application.Path
	switch journal.Request.CandidateRole {
	case RoleCLI:
		component = PackageCLIPath(manifest.Platform, manifest.PackageKind)
	case RoleServer:
		component = manifest.Components.Server.Path
	}
	executable := installedComponentPath(journal.Request.InstallRoot, journal.Previous.VersionPath, component)
	for _, name := range []string{"rollback-ready.json", "recovery-ready.json"} {
		data, err := readBoundedFile(filepath.Join(directory, name), 4096)
		var ready struct {
			PID        int    `json:"pid"`
			Version    string `json:"version"`
			Executable string `json:"executable"`
		}
		if err != nil || json.Unmarshal(data, &ready) != nil || ready.PID <= 0 ||
			ready.Version != journal.Previous.ActiveVersion || !samePath(ready.Executable, executable) {
			continue
		}
		// The nonce is not persisted; the install's private transaction directory protects this readiness evidence.
		token, err := captureProcessToken(ready.PID, executable)
		if err != nil || processGone(ready.PID) {
			continue
		}
		current, err := captureProcessToken(ready.PID, executable)
		if err == nil && current == token {
			return true
		}
	}
	return false
}

func priorManagedServiceReady(journal ApplyJournal) bool {
	if journal.Previous == nil || !hasManagedService(journal.Request) {
		return false
	}
	current, err := readStateUnchecked(journal.Request.InstallRoot)
	if err != nil || !statesEqual(current, *journal.Previous) {
		return false
	}
	manifest, err := installedManifest(journal.Request.InstallRoot, *journal.Previous)
	if err != nil {
		return false
	}
	executable := installedComponentPath(journal.Request.InstallRoot, journal.Previous.VersionPath, PackageCLIPath(manifest.Platform, manifest.PackageKind))
	record, err := ReadManagedService(journal.Request.InstallRoot)
	return err == nil && samePath(record.Executable, executable)
}

func relaunchRollbackPrior(request ApplyRequest, previous InstallState) error {
	if err := launchAndAwaitReady(request, previous, nil); err != nil {
		return fmt.Errorf("previous app relaunch failed: %w", err)
	}
	if transactionTestHook != nil {
		if err := transactionTestHook("rollback-app-ready"); err != nil {
			return err
		}
	}
	if hasManagedService(request) {
		if err := launchManagedService(request, previous, nil); err != nil {
			return fmt.Errorf("previous managed service relaunch failed: %w", err)
		}
		if transactionTestHook != nil {
			return transactionTestHook("rollback-service-ready")
		}
	}
	return nil
}
