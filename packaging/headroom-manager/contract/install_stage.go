package contract

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

func InstallArchive(archive, installRoot, entryPath string, expected Expectations) (InstallState, error) {
	return InstallArchiveWithCLIEntry(archive, installRoot, entryPath, "", expected)
}

func InstallArchiveWithCLIEntry(archive, installRoot, entryPath, cliEntryPath string, expected Expectations) (InstallState, error) {
	stage, err := StageArchive(archive, installRoot, expected)
	if err != nil {
		return InstallState{}, err
	}
	defer os.RemoveAll(filepath.Dir(filepath.Dir(stage.PackageRoot)))
	return installVerifiedStage(stage, installTargets{root: installRoot, entry: entryPath, cliEntry: cliEntryPath}, nil)
}

func installVerifiedStage(stage StageResult, targets installTargets, baseline *string) (InstallState, error) {
	installRoot, err := ValidateInstallRoot(targets.root)
	if err != nil {
		return InstallState{}, err
	}
	entryPath, cliEntryPath := targets.entry, targets.cliEntry
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
	lock, err := acquireInstallLock(installRoot, 10*time.Second)
	if err != nil {
		return InstallState{}, err
	}
	defer lock.Close()
	if baseline != nil {
		current, digestErr := installStateDigest(installRoot)
		if digestErr != nil || current != *baseline {
			return InstallState{}, errors.New("installed state changed before direct reinstall")
		}
		if err = rejectPreparedReinstall(installRoot); err != nil {
			return InstallState{}, err
		}
	}
	recoveryErr := recoverInstallLocked(installRoot)
	stage, manifest, err := LoadVerifiedStage(installRoot, stageRecordPath(stage))
	if err != nil {
		return InstallState{}, err
	}
	managedCLIEntry := cliEntryPath
	if manifest.PackageKind == PackageKindCLI {
		managedCLIEntry = entryPath
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
	}
	if managedCLIEntry != "" && manifest.Platform != "windows" {
		state.ServerEntryPath = filepath.Join(filepath.Dir(managedCLIEntry), "usage-server")
	}
	journal := InstallJournal{Schema: SchemaVersion, Product: "Headroom", Phase: journalPhaseGenerationReady, InstallRoot: installRoot,
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
		// Recovery cannot reclaim a generation until its journal exists.
		_ = os.RemoveAll(generation)
		_ = os.RemoveAll(backup)
		return InstallState{}, err
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("install-generation-ready"); err != nil {
			return InstallState{}, err
		}
	}
	if err = prepareBootstrapBackupWithCLI(installRoot, entryPath, managedCLIEntry, backup); err != nil {
		_ = os.RemoveAll(generation)
		return InstallState{}, err
	}
	journal.Phase = journalPhaseBootstrapIntent
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
	if transactionTestHook != nil {
		if err = transactionTestHook("install-bootstrap-applied"); err != nil {
			return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
		}
	}
	journal.Phase = journalPhaseStateIntent
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("install-state-intent"); err != nil {
			return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
		}
	}
	if err = writeDurableJSON(statePath, state); err != nil {
		return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("install-state-applied"); err != nil {
			return InstallState{}, errors.Join(err, recoverInstallJournal(journal, backup))
		}
	}
	journal.Phase = journalPhaseComplete
	if err = writeDurableJSON(journalPath, journal); err != nil {
		return InstallState{}, err
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("install-complete"); err != nil {
			return InstallState{}, err
		}
	}
	_ = os.Remove(filepath.Join(installRoot, "last-apply-result.json"))
	_ = os.RemoveAll(backup)
	for _, directory := range superseded {
		_ = os.RemoveAll(directory)
	}
	return state, nil
}
