package contract

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const StateName = "install-state.json"

type InstallState struct {
	PackageKind          string `json:"package_kind,omitempty"`
	Schema               int    `json:"schema"`
	Product              string `json:"product"`
	Platform             string `json:"platform"`
	Architecture         string `json:"architecture"`
	ActiveVersion        string `json:"active_version"`
	VersionPath          string `json:"version_path"`
	ManifestSHA256       string `json:"manifest_sha256"`
	PackageAsset         string `json:"package_asset"`
	ApplicationEntryPath string `json:"application_entry_path,omitempty"`
	CLIEntryPath         string `json:"cli_entry_path,omitempty"`
	ServerEntryPath      string `json:"server_entry_path,omitempty"`
}

type StageResult struct {
	// Set only after public acquisition verifies the release archive. Older and
	// manually staged packages remain valid for apply, but cannot be reused by update checks.
	ArchiveSHA256  string `json:"archive_sha256,omitempty"`
	ArchiveSize    int64  `json:"archive_size,omitempty"`
	PackageKind    string `json:"package_kind,omitempty"`
	Schema         int    `json:"schema"`
	Product        string `json:"product"`
	Version        string `json:"version"`
	Platform       string `json:"platform"`
	Architecture   string `json:"architecture"`
	PackageAsset   string `json:"package_asset"`
	ManifestSHA256 string `json:"manifest_sha256"`
	PackageRoot    string `json:"package_root"`
}

type Inspection struct {
	PackageKind      string   `json:"package_kind,omitempty"`
	Installed        bool     `json:"installed"`
	TrustedIdentity  bool     `json:"trusted_identity"`
	Complete         bool     `json:"complete"`
	Version          string   `json:"version,omitempty"`
	VersionPath      string   `json:"version_path,omitempty"`
	Platform         string   `json:"platform,omitempty"`
	Architecture     string   `json:"architecture,omitempty"`
	PackageAsset     string   `json:"package_asset,omitempty"`
	LauncherPath     string   `json:"launcher_path,omitempty"`
	CLIEntryPath     string   `json:"cli_entry_path,omitempty"`
	ServerEntryPath  string   `json:"server_entry_path,omitempty"`
	ActiveExecutable string   `json:"active_executable,omitempty"`
	Missing          []string `json:"missing,omitempty"`
	ApplyStatus      string   `json:"apply_status,omitempty"`
	ApplyMessage     string   `json:"apply_message,omitempty"`
}

func StageArchive(archive, installRoot string, expected Expectations) (StageResult, error) {
	var err error
	installRoot, err = NormalizeInstallRoot(installRoot)
	if err != nil {
		return StageResult{}, err
	}
	if err := validateInstallTargets(installRoot, ""); err != nil {
		return StageResult{}, err
	}
	platform, architecture, err := NativeTarget()
	if err != nil {
		return StageResult{}, err
	}
	if expected.Platform != "" && expected.Platform != platform {
		return StageResult{}, fmt.Errorf("cannot stage %s package on %s", expected.Platform, platform)
	}
	if expected.Architecture != "" && expected.Architecture != architecture {
		return StageResult{}, fmt.Errorf("cannot stage %s package on %s", expected.Architecture, architecture)
	}
	inspected, _, err := InspectArchive(archive)
	if err != nil {
		return StageResult{}, err
	}
	if err = CheckExpectations(inspected, expected); err != nil {
		return StageResult{}, err
	}
	if inspected.Platform != platform || inspected.Architecture != architecture {
		return StageResult{}, fmt.Errorf("cannot stage %s/%s package on %s/%s", inspected.Platform, inspected.Architecture, platform, architecture)
	}
	expected.Platform = platform
	expected.Architecture = architecture
	stagingRoot, err := ensureOwnedDirectory(installRoot, "staging", 0o700)
	if err != nil {
		return StageResult{}, err
	}
	if err = validateInstallTargets(stagingRoot, ""); err != nil {
		return StageResult{}, err
	}
	token := make([]byte, 8)
	if _, err = rand.Read(token); err != nil {
		return StageResult{}, err
	}
	stage := filepath.Join(stagingRoot, "package-"+hex.EncodeToString(token))
	extractRoot := filepath.Join(stage, "contents")
	if err = os.Mkdir(stage, 0o700); err != nil {
		return StageResult{}, err
	}
	manifest, packageRoot, err := ExtractAndVerify(archive, extractRoot, expected)
	if err != nil {
		os.RemoveAll(stage)
		return StageResult{}, err
	}
	manifestHash, err := digestFile(filepath.Join(packageRoot, PackageManifestName))
	if err != nil {
		os.RemoveAll(stage)
		return StageResult{}, err
	}
	result := StageResult{PackageKind: manifest.PackageKind, Schema: SchemaVersion, Product: "Headroom", Version: manifest.Version, Platform: manifest.Platform, Architecture: manifest.Architecture, PackageAsset: manifest.AssetName, ManifestSHA256: manifestHash, PackageRoot: packageRoot}
	if err = WriteJSON(filepath.Join(stage, "verified-stage.json"), result); err != nil {
		os.RemoveAll(stage)
		return StageResult{}, err
	}
	return result, nil
}

func InstallArchive(archive, installRoot, entryPath string, expected Expectations) (InstallState, error) {
	return InstallArchiveWithCLIEntry(archive, installRoot, entryPath, "", expected)
}

func InstallArchiveWithCLIEntry(archive, installRoot, entryPath, cliEntryPath string, expected Expectations) (InstallState, error) {
	var err error
	installRoot, err = NormalizeInstallRoot(installRoot)
	if err != nil {
		return InstallState{}, err
	}
	if entryPath != "" {
		entryPath, err = normalizeAbsolutePath(entryPath, "entry path")
		if err != nil {
			return InstallState{}, err
		}
	}
	if cliEntryPath != "" {
		cliEntryPath, err = normalizeAbsolutePath(cliEntryPath, "CLI entry path")
		if err != nil {
			return InstallState{}, err
		}
	}
	if err = validateInstallTargets(installRoot, entryPath); err != nil {
		return InstallState{}, err
	}
	if err = validateInstallTargets(installRoot, cliEntryPath); err != nil {
		return InstallState{}, err
	}
	stage, err := StageArchive(archive, installRoot, expected)
	if err != nil {
		return InstallState{}, err
	}
	stageContainer := filepath.Dir(filepath.Dir(stage.PackageRoot))
	defer os.RemoveAll(stageContainer)
	recoveryErr := RecoverInstall(installRoot)
	record := filepath.Join(stageContainer, "verified-stage.json")
	stage, manifest, err := LoadVerifiedStage(installRoot, record)
	if err != nil {
		return InstallState{}, err
	}
	managedCLIEntry := cliEntryPath
	if manifest.PackageKind == PackageKindCLI {
		managedCLIEntry = entryPath
	}
	lock, err := acquireInstallLock(installRoot, 10*time.Second)
	if err != nil {
		return InstallState{}, err
	}
	defer lock.Close()
	stage, manifest, err = LoadVerifiedStage(installRoot, record)
	if err != nil {
		return InstallState{}, err
	}
	var superseded []string
	if recoveryErr != nil {
		superseded, err = supersedeUnrecoverableTransactions(installRoot)
		if err != nil {
			return InstallState{}, errors.Join(recoveryErr, err)
		}
	} else if err = rejectIncompleteTransactions(installRoot); err != nil {
		return InstallState{}, err
	}
	if managedCLIEntry != "" && manifest.Platform != "windows" {
		serverEntry := filepath.Join(filepath.Dir(managedCLIEntry), "usage-server")
		if info, statErr := os.Lstat(serverEntry); statErr == nil {
			prior, priorErr := readTrustedState(installRoot, true)
			if priorErr != nil || prior == nil || !info.Mode().IsRegular() || !samePath(prior.ServerEntryPath, serverEntry) {
				return InstallState{}, errors.New("existing usage-server compatibility path is not managed by this Headroom installation")
			}
			priorManifest, manifestErr := installedManifest(installRoot, *prior)
			recordPath := "bootstrap/headroom-cli"
			if prior.PackageKind == PackageKindCLI {
				recordPath = "bootstrap/headroom"
			}
			if manifestErr != nil || verifyStableEntry(installRoot, serverEntry, priorManifest, recordPath) != nil {
				return InstallState{}, errors.New("existing usage-server compatibility path is not managed by this Headroom installation")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return InstallState{}, statErr
		}
	}
	transactions, err := ensureOwnedDirectory(installRoot, "transactions", 0o700)
	if err != nil {
		return InstallState{}, err
	}
	token, err := randomHex(8)
	if err != nil {
		return InstallState{}, err
	}
	backup := filepath.Join(transactions, "install-"+token)
	if err = os.Mkdir(backup, 0o700); err != nil {
		return InstallState{}, err
	}
	state, generation, err := createGeneration(installRoot, stage, manifest)
	if err != nil {
		_ = os.RemoveAll(backup)
		return InstallState{}, err
	}
	if manifest.PackageKind == PackageKindCLI {
		state.CLIEntryPath = entryPath
	} else {
		state.ApplicationEntryPath = entryPath
		state.CLIEntryPath = cliEntryPath
		if cliEntryPath != "" && manifest.Platform != "windows" {
			state.ServerEntryPath = filepath.Join(filepath.Dir(cliEntryPath), "usage-server")
		}
	}
	if managedCLIEntry != "" && manifest.Platform != "windows" {
		state.ServerEntryPath = filepath.Join(filepath.Dir(managedCLIEntry), "usage-server")
	}
	journal := InstallJournal{Schema: SchemaVersion, Product: "Headroom", Phase: "generation-ready", InstallRoot: installRoot,
		EntryPath: entryPath, CLIEntryPath: managedCLIEntry, GenerationDir: generation, Candidate: state}
	for _, directory := range superseded {
		journal.Supersedes = append(journal.Supersedes, filepath.Base(directory))
	}
	statePath := filepath.Join(installRoot, StateName)
	if info, stateErr := os.Lstat(statePath); stateErr == nil {
		if !info.Mode().IsRegular() {
			_ = os.RemoveAll(generation)
			_ = os.RemoveAll(backup)
			return InstallState{}, errors.New("installed state path is unsafe")
		}
		journal.PriorStateExisted = true
		journal.PriorStateSHA256, err = digestFile(statePath)
		if err != nil {
			_ = os.RemoveAll(generation)
			_ = os.RemoveAll(backup)
			return InstallState{}, err
		}
		if err = copyFile(statePath, filepath.Join(backup, "prior-install-state.json"), 0o600); err != nil {
			_ = os.RemoveAll(generation)
			_ = os.RemoveAll(backup)
			return InstallState{}, err
		}
	} else if !errors.Is(stateErr, os.ErrNotExist) {
		_ = os.RemoveAll(generation)
		_ = os.RemoveAll(backup)
		return InstallState{}, stateErr
	}
	journalPath := filepath.Join(backup, InstallJournalName)
	if err = writeDurableJSON(journalPath, journal); err != nil {
		// No journal exists yet, so recovery cannot reclaim these; remove them here.
		_ = os.RemoveAll(generation)
		_ = os.RemoveAll(backup)
		return InstallState{}, err
	}
	if err = prepareBootstrapBackupWithCLI(installRoot, entryPath, managedCLIEntry, backup); err != nil {
		_ = os.RemoveAll(generation)
		return InstallState{}, err
	}
	journal.Phase = "bootstrap-intent"
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, err
	}
	if transactionTestHook != nil {
		if hookErr := transactionTestHook("install-bootstrap-intent"); hookErr != nil {
			return InstallState{}, errors.Join(hookErr, recoverInstallJournal(journal, backup))
		}
	}
	if err = applyBootstrapWithCLI(stage.PackageRoot, installRoot, entryPath, managedCLIEntry, manifest); err != nil {
		return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
	}
	journal.Phase = "state-intent"
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
	}
	if err = writeDurableJSON(statePath, state); err != nil {
		return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
	}
	journal.Phase = "complete"
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, err
	}
	_ = os.Remove(filepath.Join(installRoot, "last-apply-result.json"))
	_ = os.RemoveAll(backup)
	for _, directory := range superseded {
		_ = os.RemoveAll(directory)
	}
	return state, nil
}

// MigrateManagedEntries installs the public CLI router from an already verified
// desktop generation. It is used after an older manager has installed a new
// generation but did not know about the CLI bootstrap record.
func MigrateManagedEntries(installRoot, applicationEntry, cliEntry string, replaceLegacyServer bool) (InstallState, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return InstallState{}, err
	}
	applicationEntry, err = normalizeAbsolutePath(applicationEntry, "application entry path")
	if err != nil {
		return InstallState{}, err
	}
	cliEntry, err = normalizeAbsolutePath(cliEntry, "CLI entry path")
	if err != nil {
		return InstallState{}, err
	}
	if err = validateInstallTargets(root, applicationEntry); err != nil {
		return InstallState{}, err
	}
	if err = validateInstallTargets(root, cliEntry); err != nil {
		return InstallState{}, err
	}
	if err = RecoverInstall(root); err != nil {
		return InstallState{}, err
	}
	lock, err := acquireInstallLock(root, 10*time.Second)
	if err != nil {
		return InstallState{}, err
	}
	defer lock.Close()
	state, err := readTrustedState(root, true)
	if err != nil || state == nil {
		return InstallState{}, errors.New("installed desktop generation is not trusted")
	}
	if state.PackageKind == PackageKindCLI {
		return InstallState{}, errors.New("CLI-only installations do not need desktop entry migration")
	}
	manifest, err := installedManifest(root, *state)
	if err != nil {
		return InstallState{}, err
	}
	ext := ""
	if manifest.Platform == "windows" {
		ext = ".exe"
	}
	routerRecord, routerOK := fileRecord(manifest.Files, "bundle/bin/headroom-cli-launcher"+ext)
	bootstrapRecord, bootstrapOK := fileRecord(manifest.Files, "bootstrap/headroom-cli"+ext)
	if !routerOK || !bootstrapOK || routerRecord.LinkTarget != "" || bootstrapRecord.LinkTarget != "" || routerRecord.SHA256 != bootstrapRecord.SHA256 || routerRecord.Size != bootstrapRecord.Size {
		return InstallState{}, errors.New("installed generation has no verified CLI migration router")
	}
	router := filepath.Join(root, filepath.FromSlash(state.VersionPath), "bin", "headroom-cli-launcher"+ext)
	if digest, digestErr := digestFile(router); digestErr != nil || digest != routerRecord.SHA256 {
		return InstallState{}, errors.New("installed CLI migration router failed verification")
	}
	serverEntry := ""
	if manifest.Platform != "windows" {
		serverEntry = filepath.Join(filepath.Dir(cliEntry), "usage-server")
	}
	if state.ApplicationEntryPath == applicationEntry && state.CLIEntryPath == cliEntry && state.ServerEntryPath == serverEntry {
		inspection := InspectInstall(root)
		if inspection.TrustedIdentity && inspection.Complete {
			return *state, nil
		}
	}
	applicationMissing := false
	if err = verifyStableEntry(root, applicationEntry, manifest, "bootstrap/headroom"+ext); err != nil {
		if _, statErr := os.Lstat(applicationEntry); !errors.Is(statErr, os.ErrNotExist) {
			return InstallState{}, errors.New("application entry does not match the trusted legacy launcher")
		}
		applicationMissing = true
	}
	cliIsLegacyGUI := false
	if info, statErr := os.Lstat(cliEntry); statErr == nil {
		if !info.Mode().IsRegular() {
			return InstallState{}, errors.New("existing public CLI path is unsafe")
		}
		if currentErr := verifyStableEntry(root, cliEntry, manifest, "bootstrap/headroom-cli"+ext); currentErr != nil {
			if legacyErr := verifyStableEntry(root, cliEntry, manifest, "bootstrap/headroom"+ext); legacyErr != nil {
				return InstallState{}, errors.New("existing public CLI path is not the managed legacy GUI launcher")
			}
			cliIsLegacyGUI = true
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return InstallState{}, statErr
	}
	if manifest.Platform != "windows" {
		if info, statErr := os.Lstat(serverEntry); statErr == nil {
			if currentErr := verifyStableEntry(root, serverEntry, manifest, "bootstrap/headroom-cli"); currentErr == nil {
				// Already the managed compatibility router.
			} else if !replaceLegacyServer || !info.Mode().IsRegular() {
				return InstallState{}, errors.New("existing usage-server compatibility path requires explicit legacy replacement")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return InstallState{}, statErr
		}
	}
	backupDir, err := os.MkdirTemp(filepath.Join(root, "transactions"), "migrate-entries-")
	if err != nil {
		return InstallState{}, err
	}
	if err = prepareBootstrapBackupWithCLI(root, applicationEntry, cliEntry, backupDir); err != nil {
		_ = os.RemoveAll(backupDir)
		return InstallState{}, err
	}
	updated := *state
	updated.ApplicationEntryPath, updated.CLIEntryPath, updated.ServerEntryPath = applicationEntry, cliEntry, serverEntry
	journal := EntryMigrationJournal{Schema: SchemaVersion, Product: "Headroom", Phase: "entry-intent", InstallRoot: root,
		EntryPath: applicationEntry, CLIEntryPath: cliEntry, Previous: *state, Candidate: updated}
	journalPath := filepath.Join(backupDir, EntryMigrationJournalName)
	if err = writeDurableJSON(journalPath, journal); err != nil {
		_ = os.RemoveAll(backupDir)
		return InstallState{}, err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, restoreBootstrapWithCLI(root, applicationEntry, cliEntry, backupDir), writeDurableJSON(filepath.Join(root, StateName), state))
	}
	if applicationMissing {
		if !cliIsLegacyGUI {
			return InstallState{}, rollback(errors.New("missing application entry has no trusted legacy source"))
		}
		if err = replaceFile(cliEntry, applicationEntry, 0o755); err != nil {
			return InstallState{}, rollback(err)
		}
		if err = replaceBytes([]byte(root+"\n"), applicationEntry+".root", 0o600); err != nil {
			return InstallState{}, rollback(err)
		}
	}
	if err = replaceFile(router, cliEntry, 0o755); err != nil {
		return InstallState{}, rollback(err)
	}
	if err = replaceBytes([]byte(root+"\n"), cliEntry+".root", 0o600); err != nil {
		return InstallState{}, rollback(err)
	}
	if serverEntry != "" {
		if err = replaceFile(router, serverEntry, 0o755); err != nil {
			return InstallState{}, rollback(err)
		}
		if err = replaceBytes([]byte(root+"\n"), serverEntry+".root", 0o600); err != nil {
			return InstallState{}, rollback(err)
		}
	}
	journal.Phase = "state-intent"
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, rollback(err)
	}
	if err = writeDurableJSON(filepath.Join(root, StateName), updated); err != nil {
		return InstallState{}, rollback(err)
	}
	if inspection := InspectInstall(root); !inspection.TrustedIdentity || !inspection.Complete {
		return InstallState{}, rollback(errors.New("migrated entries failed verification"))
	}
	journal.Phase = "complete"
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, err
	}
	_ = os.RemoveAll(backupDir)
	return updated, nil
}

func InspectInstall(installRoot string) Inspection {
	result := Inspection{}
	installRoot, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return result
	}
	data, err := readBoundedFile(filepath.Join(installRoot, StateName), maxManifestBytes)
	if err != nil {
		return result
	}
	var state InstallState
	if json.Unmarshal(data, &state) != nil {
		return result
	}
	result, _ = inspectState(installRoot, state)
	var outcome ApplyResult
	if data, readErr := readBoundedFile(filepath.Join(installRoot, "last-apply-result.json"), maxManifestBytes); readErr == nil && json.Unmarshal(data, &outcome) == nil &&
		outcome.Schema == SchemaVersion && outcome.Product == "Headroom" && outcome.Version == state.ActiveVersion && outcome.VersionPath == state.VersionPath {
		result.ApplyStatus, result.ApplyMessage = outcome.Status, outcome.FailureReason
	}
	return result
}

func inspectState(installRoot string, state InstallState) (Inspection, error) {
	result := Inspection{Installed: true}
	if err := validateInstallTargets(installRoot, ""); err != nil {
		return result, err
	}
	if state.Schema != SchemaVersion || state.Product != "Headroom" || !validVersion(state.ActiveVersion) || !validVersionPath(state) {
		return result, errors.New("installed state identity is invalid")
	}
	for _, entry := range []string{state.ApplicationEntryPath, state.CLIEntryPath, state.ServerEntryPath} {
		if entry == "" {
			continue
		}
		normalized, pathErr := normalizeAbsolutePath(entry, "installed entry path")
		if pathErr != nil || normalized != entry || validateInstallTargets(installRoot, entry) != nil {
			return result, errors.New("installed entry path is invalid")
		}
	}
	expectedAsset, err := AssetNameForKind(state.PackageKind, state.ActiveVersion, state.Platform, state.Architecture)
	if err != nil || expectedAsset != state.PackageAsset || !hashPattern.MatchString(state.ManifestSHA256) {
		return result, errors.New("installed state package identity is invalid")
	}
	manifestPath := filepath.Join(installRoot, filepath.FromSlash(state.VersionPath), PackageManifestName)
	if err := validateInstallTargets(manifestPath, ""); err != nil {
		return result, err
	}
	digest, err := digestFile(manifestPath)
	if err != nil || digest != state.ManifestSHA256 {
		return result, errors.New("installed manifest digest is invalid")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return result, err
	}
	manifest, err := DecodePackageManifest(file)
	file.Close()
	if err != nil || manifest.PackageKind != state.PackageKind || manifest.Version != state.ActiveVersion || manifest.Platform != state.Platform || manifest.Architecture != state.Architecture || manifest.AssetName != state.PackageAsset {
		return result, errors.New("installed manifest identity is invalid")
	}
	result.TrustedIdentity = true
	result.PackageKind = state.PackageKind
	result.Version, result.VersionPath = state.ActiveVersion, state.VersionPath
	result.Platform, result.Architecture, result.PackageAsset = state.Platform, state.Architecture, state.PackageAsset
	ext := ""
	if state.Platform == "windows" {
		ext = ".exe"
	}
	result.LauncherPath = filepath.Join(installRoot, "headroom-launcher"+ext)
	if state.Platform == "windows" {
		result.LauncherPath = filepath.Join(installRoot, "headroom.exe")
	}
	if state.ApplicationEntryPath != "" {
		result.LauncherPath = state.ApplicationEntryPath
	}
	result.CLIEntryPath = state.CLIEntryPath
	result.ServerEntryPath = state.ServerEntryPath
	versionRoot := filepath.Join(installRoot, filepath.FromSlash(state.VersionPath))
	result.ActiveExecutable = installedComponentPath(installRoot, state.VersionPath, manifest.Components.Application.Path)
	for _, record := range manifest.Files {
		if !strings.HasPrefix(record.Path, "bundle/") {
			continue
		}
		relative := strings.TrimPrefix(record.Path, "bundle/")
		name := filepath.Join(versionRoot, filepath.FromSlash(relative))
		// Framework symlinks are allowed only as declared leaf records. Never
		// follow a replaced directory while verifying installed content.
		if err := validateInstallTargets(filepath.Dir(name), ""); err != nil {
			result.TrustedIdentity = false
			return result, err
		}
		info, statErr := os.Lstat(name)
		if record.LinkTarget != "" {
			target, linkErr := os.Readlink(name)
			if statErr != nil || linkErr != nil || info.Mode()&os.ModeSymlink == 0 || target != record.LinkTarget {
				result.Missing = append(result.Missing, relative)
			}
			continue
		}
		modeMismatch := state.Platform != "windows" && fmt.Sprintf("%04o", infoMode(info)) != record.Mode
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != record.Size || modeMismatch {
			result.Missing = append(result.Missing, relative)
			continue
		}
		fileDigest, digestErr := digestFile(name)
		if digestErr != nil || fileDigest != record.SHA256 {
			result.Missing = append(result.Missing, relative)
		}
	}
	bootstrapMissing := func(recordPath, installedPath string) {
		for _, record := range manifest.Files {
			if record.Path != recordPath {
				continue
			}
			info, statErr := os.Lstat(installedPath)
			digest, digestErr := digestFile(installedPath)
			if statErr != nil || !info.Mode().IsRegular() || info.Size() != record.Size || digestErr != nil || digest != record.SHA256 {
				result.Missing = append(result.Missing, recordPath)
			}
			return
		}
	}
	if state.Platform == "windows" {
		bootstrapMissing("bootstrap/headroom.exe", filepath.Join(installRoot, "headroom.exe"))
		bootstrapMissing("bootstrap/headroom-package.exe", filepath.Join(installRoot, "headroom-package.exe"))
	} else {
		bootstrapMissing("bootstrap/headroom", filepath.Join(installRoot, "headroom-launcher"))
		bootstrapMissing("bootstrap/headroom-package", filepath.Join(installRoot, "headroom-package"))
	}
	if data, associationErr := readBoundedFile(result.LauncherPath+".root", 4096); associationErr != nil || string(data) != installRoot+"\n" {
		result.Missing = append(result.Missing, "bootstrap/association")
	}
	if state.CLIEntryPath != "" {
		record := "bootstrap/headroom-cli"
		if state.Platform == "windows" {
			record += ".exe"
		}
		if state.PackageKind == PackageKindCLI {
			record = "bootstrap/headroom"
			if state.Platform == "windows" {
				record += ".exe"
			}
		}
		bootstrapMissing(record, state.CLIEntryPath)
		if data, associationErr := readBoundedFile(state.CLIEntryPath+".root", 4096); associationErr != nil || string(data) != installRoot+"\n" {
			result.Missing = append(result.Missing, "bootstrap/cli-association")
		}
		if state.ServerEntryPath != "" {
			bootstrapMissing(record, state.ServerEntryPath)
			if data, associationErr := readBoundedFile(state.ServerEntryPath+".root", 4096); associationErr != nil || string(data) != installRoot+"\n" {
				result.Missing = append(result.Missing, "bootstrap/server-association")
			}
		}
	}
	result.Complete = len(result.Missing) == 0
	return result, nil
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func validVersionPath(state InstallState) bool {
	legacy := filepath.ToSlash(filepath.Join("versions", state.ActiveVersion))
	if state.VersionPath == legacy {
		return true
	}
	prefix := legacy + ".generation-"
	if !strings.HasPrefix(state.VersionPath, prefix) || strings.Contains(state.VersionPath, "\\") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(state.VersionPath))) != state.VersionPath {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(state.VersionPath, prefix), "-")
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 16 {
		return false
	}
	isHex := func(value string) bool {
		_, err := hex.DecodeString(value)
		return err == nil && strings.ToLower(value) == value
	}
	return isHex(parts[0]) && isHex(parts[1]) && strings.HasPrefix(state.ManifestSHA256, parts[0])
}

func ActiveExecutable(installRoot string) (string, Inspection, error) {
	return ActiveExecutableForRole(installRoot, RoleApplication)
}

func ManagerExecutable(installRoot string) (string, Inspection, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return "", Inspection{}, err
	}
	inspection := InspectInstall(root)
	if !inspection.TrustedIdentity {
		return "", inspection, errors.New("Headroom installation identity is not valid")
	}
	manifest, err := installedManifest(root, InstallState{Schema: SchemaVersion, Product: "Headroom", ActiveVersion: inspection.Version, VersionPath: inspection.VersionPath})
	if err != nil {
		return "", inspection, err
	}
	ext := ""
	if manifest.Platform == "windows" {
		ext = ".exe"
	}
	path := filepath.Join(root, "headroom-package"+ext)
	record, ok := fileRecord(manifest.Files, "bootstrap/headroom-package"+ext)
	if !ok || record.LinkTarget != "" {
		return "", inspection, errors.New("manager bootstrap is not inventoried")
	}
	if err = validateInstallTargets(root, path); err != nil {
		return "", inspection, err
	}
	info, statErr := os.Lstat(path)
	digest, digestErr := digestFile(path)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() != record.Size || digestErr != nil || digest != record.SHA256 {
		return "", inspection, errors.New("installed manager failed verification")
	}
	return path, inspection, nil
}

const (
	RoleApplication     = "application"
	RoleCLI             = "cli"
	RoleServer          = "server"
	RolePublicLauncher  = "public_launcher"
	RoleManagedServer   = "managed_server"
	RoleManagedLauncher = "managed_launcher"
)

// ActiveExecutableForRole resolves a fixed, inventoried component. A caller
// cannot make an arbitrary path trusted by labeling it an update participant.
func ActiveExecutableForRole(installRoot, role string) (string, Inspection, error) {
	canonicalRoot, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return "", Inspection{}, err
	}
	installRoot = canonicalRoot
	inspection := InspectInstall(installRoot)
	if !inspection.TrustedIdentity {
		return "", inspection, errors.New("Headroom installation identity is not valid")
	}
	manifestFile, manifestErr := os.Open(filepath.Join(installRoot, filepath.FromSlash(inspection.VersionPath), PackageManifestName))
	if manifestErr != nil {
		return "", inspection, manifestErr
	}
	manifest, manifestErr := DecodePackageManifest(manifestFile)
	manifestFile.Close()
	if manifestErr != nil {
		return "", inspection, manifestErr
	}
	packagePath := manifest.Components.Application.Path
	switch role {
	case "", RoleApplication:
	case RoleCLI, RoleManagedServer:
		packagePath = PackageCLIPath(manifest.Platform, manifest.PackageKind)
	case RoleServer:
		packagePath = manifest.Components.Server.Path
	case RolePublicLauncher, RoleManagedLauncher:
		if inspection.CLIEntryPath == "" {
			return "", inspection, errors.New("trusted public CLI entry is not recorded")
		}
		ext := ""
		if manifest.Platform == "windows" {
			ext = ".exe"
		}
		if err := verifyStableEntry(installRoot, inspection.CLIEntryPath, manifest, "bootstrap/headroom-cli"+ext); err != nil {
			if manifest.PackageKind == PackageKindCLI {
				err = verifyStableEntry(installRoot, inspection.CLIEntryPath, manifest, "bootstrap/headroom"+ext)
			}
			if err != nil {
				return "", inspection, err
			}
		}
		return inspection.CLIEntryPath, inspection, nil
	default:
		return "", inspection, errors.New("unknown Headroom component role")
	}
	record, listed := fileRecord(manifest.Files, packagePath)
	if !listed || record.LinkTarget != "" {
		return "", inspection, errors.New("requested Headroom component is not installed")
	}
	executable := installedComponentPath(installRoot, inspection.VersionPath, packagePath)
	for _, missing := range inspection.Missing {
		if !isAuxiliaryPath(missing) || missing == strings.TrimPrefix(packagePath, "bundle/") {
			return "", inspection, fmt.Errorf("Headroom %s runtime is incomplete; reinstall %s", inspection.Version, inspection.PackageAsset)
		}
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", inspection, fmt.Errorf("Headroom %s application is missing; reinstall %s", inspection.Version, inspection.PackageAsset)
	}
	digest, err := digestFile(executable)
	if err != nil || digest != record.SHA256 {
		return "", inspection, errors.New("requested Headroom component failed verification")
	}
	return executable, inspection, nil
}

func verifyStableEntry(root, entry string, manifest PackageManifest, recordPath string) error {
	record, ok := fileRecord(manifest.Files, recordPath)
	if !ok || record.LinkTarget != "" {
		return errors.New("public CLI bootstrap is not inventoried")
	}
	if err := validateInstallTargets(root, entry); err != nil {
		return err
	}
	info, err := os.Lstat(entry)
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.Size {
		return errors.New("public CLI entry is invalid")
	}
	digest, err := digestFile(entry)
	if err != nil || digest != record.SHA256 {
		return errors.New("public CLI entry failed verification")
	}
	association, err := readBoundedFile(entry+".root", 4096)
	if err != nil || string(association) != root+"\n" {
		return errors.New("public CLI association is invalid")
	}
	return nil
}

func isAuxiliaryPath(path string) bool {
	switch filepath.ToSlash(path) {
	case "bin/usage-server", "bin/usage-server.exe", "bin/headroom-credential-helper.exe", "Headroom.app/Contents/MacOS/usage-server",
		"bootstrap/headroom", "bootstrap/headroom.exe", "bootstrap/headroom-package", "bootstrap/headroom-package.exe", "bootstrap/association":
		return true
	}
	return false
}

// NormalizeInstallRoot accepts native absolute paths with either separator and
// returns the single path representation persisted in launcher associations.
func NormalizeInstallRoot(installRoot string) (string, error) {
	return normalizeAbsolutePath(installRoot, "install root")
}

func ValidateInstallRoot(installRoot string) (string, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return "", err
	}
	if err = validateInstallTargets(root, ""); err != nil {
		return "", err
	}
	return root, nil
}

func normalizeAbsolutePath(value, label string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s must be an absolute clean path", label)
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s must be an absolute clean path", label)
	}
	return clean, nil
}

func validateInstallTargets(installRoot, entryPath string) error {
	if !filepath.IsAbs(installRoot) || filepath.Clean(installRoot) != installRoot {
		return errors.New("install root must be an absolute clean path")
	}
	if entryPath != "" && (!filepath.IsAbs(entryPath) || filepath.Clean(entryPath) != entryPath) {
		return errors.New("entry path must be an absolute clean path")
	}
	for _, target := range []string{installRoot, entryPath} {
		if target == "" {
			continue
		}
		current := target
		for {
			info, err := os.Lstat(current)
			if err == nil && pathIsLinkOrReparse(current, info) {
				return fmt.Errorf("install target traverses a link: %s", current)
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	return nil
}

func digestFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(name)
			packagePath := "bundle/" + filepath.ToSlash(rel)
			if err != nil || !validFrameworkLink(packagePath, link) {
				return fmt.Errorf("links outside a macOS framework are forbidden: %s", rel)
			}
			resolved, resolveErr := filepath.EvalSymlinks(name)
			if resolveErr != nil {
				return resolveErr
			}
			resolved, resolveErr = filepath.Abs(resolved)
			if resolveErr != nil || !strings.HasPrefix(resolved, filepath.Clean(source)+string(os.PathSeparator)) {
				return fmt.Errorf("framework link escapes verified payload: %s", rel)
			}
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if info.Mode()&os.ModeType != 0 && !info.IsDir() {
			return fmt.Errorf("links and special files are forbidden: %s", rel)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(name, target, info.Mode().Perm())
	})
}
func copyFile(source, destination string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, mode)
}
func replaceFile(source, destination string, mode os.FileMode) error {
	src, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return replaceBytes(src, destination, mode)
}
func replaceBytes(contents []byte, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".headroom-replace-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(temporary)
		}
	}()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(contents)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = replaceAtomic(temporary, destination); err != nil {
		return err
	}
	failed = false
	return syncDirectory(filepath.Dir(destination))
}
func writeAtomicJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	temporary := name + ".new"
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, name)
}
