package contract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func RecoverInstall(installRoot string) error {
	root, err := ValidateInstallRoot(installRoot)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(root, 5*time.Second)
	if err != nil {
		return err
	}
	defer lock.Close()
	transactions := filepath.Join(root, "transactions")
	entries, err := os.ReadDir(transactions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	deferred := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "install-") {
			continue
		}
		directory := filepath.Join(transactions, entry.Name())
		journal, readErr := readInstallJournal(filepath.Join(directory, InstallJournalName), root, directory)
		if readErr == nil && journal.Phase != journalPhaseComplete && journal.Phase != journalPhaseRolledBack && journal.Phase != journalPhaseObsolete {
			for _, name := range journal.Supersedes {
				deferred[name] = true
			}
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() || deferred[entry.Name()] {
			continue
		}
		directory := filepath.Join(transactions, entry.Name())
		if strings.HasPrefix(entry.Name(), "migrate-entries-") {
			data, readErr := readBoundedFile(filepath.Join(directory, EntryMigrationJournalName), maxManifestBytes)
			var journal EntryMigrationJournal
			if readErr != nil || json.Unmarshal(data, &journal) != nil || journal.Schema != SchemaVersion || journal.Product != "Headroom" || journal.InstallRoot != root {
				continue
			}
			entryPath, entryErr := normalizeAbsolutePath(journal.EntryPath, "migration entry")
			cliPath, cliErr := normalizeAbsolutePath(journal.CLIEntryPath, "migration CLI entry")
			if entryErr != nil || cliErr != nil || entryPath != journal.EntryPath || cliPath != journal.CLIEntryPath || !validVersionPath(journal.Previous) || !validVersionPath(journal.Candidate) {
				continue
			}
			current, stateErr := readStateUnchecked(root)
			if stateErr == nil && statesEqual(current, journal.Candidate) {
				inspection := InspectInstall(root)
				if !inspection.TrustedIdentity || !inspection.Complete {
					return errors.New("interrupted entry migration candidate is incomplete")
				}
				_ = os.RemoveAll(directory)
				continue
			}
			if stateErr == nil && statesEqual(current, journal.Previous) {
				if err = restoreBootstrapWithCLI(root, journal.EntryPath, journal.CLIEntryPath, directory); err != nil {
					return err
				}
				_ = os.RemoveAll(directory)
				continue
			}
			return errors.New("entry migration state requires manual recovery")
		}
		if strings.HasPrefix(entry.Name(), "install-") {
			journal, readErr := readInstallJournal(filepath.Join(directory, InstallJournalName), root, directory)
			if readErr != nil {
				continue
			}
			if journal.Phase == journalPhaseComplete || journal.Phase == journalPhaseRolledBack {
				_ = os.RemoveAll(directory)
				continue
			}
			if journal.Phase == journalPhaseObsolete {
				continue
			}
			if err = recoverInstallJournal(journal, directory); err != nil {
				return err
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), "apply-") {
			continue
		}
		journal, readErr := readJournal(filepath.Join(directory, TransactionJournalName))
		if readErr != nil {
			continue
		}
		if journal.Phase == journalPhasePrepared {
			request, _, requestErr := readApplyRequest(filepath.Join(directory, TransactionRequestName))
			if requestErr != nil || !applyRequestsEqual(request, journal.Request) {
				continue
			}
		} else if validateApplyJournal(journal, root, directory) != nil {
			continue
		}
		if err = recoverApplyJournal(journal, directory); err != nil {
			return err
		}
	}
	return nil
}
