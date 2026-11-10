package contract

import (
	"os"
	"path/filepath"
	"time"
)

func recoverApplyJournal(journal ApplyJournal, directory string) error {
	root := journal.Request.InstallRoot
	stops := planApplyRecovery(journal, recoveryStateMatch{})
	if stops.stopCandidate {
		if err := stopRecordedProcess(journal.CandidatePID, journal.CandidateExe, journal.CandidateToken, 5*time.Second); err != nil {
			return err
		}
	}
	switch stops.stopService {
	case recoveryServiceKeep:
	case recoveryServiceStopProcess:
		if err := stopRecordedProcess(journal.ServicePID, journal.ServiceExe, journal.ServiceToken, 5*time.Second); err != nil {
			return err
		}
	case recoveryServiceStopSupervisor, recoveryServiceStopAndClearSupervisor:
		if err := stopManagedServiceSupervisor(journal.Request.ManagedServiceSupervisor); err != nil {
			return err
		}
		if stops.stopService == recoveryServiceStopAndClearSupervisor {
			if err := clearSupervisedManagedServiceRestart(root); err != nil {
				return err
			}
		}
	}
	current, stateErr := readStateUnchecked(root)
	plan := planApplyRecovery(journal, recoveryStateMatch{
		prior:     stateErr == nil && journal.Previous != nil && statesEqual(current, *journal.Previous),
		candidate: stateErr == nil && journal.Candidate != nil && statesEqual(current, *journal.Candidate),
	})
	switch plan.action {
	case recoveryDiscard:
		if plan.recordApplied {
			if err := ensureAppliedResult(root, *journal.Candidate); err != nil {
				return err
			}
		}
		_ = os.RemoveAll(directory)
		return nil
	case recoveryObsolete:
		journal.Phase = journalPhaseObsolete
		_ = writeDurableJSON(filepath.Join(directory, TransactionJournalName), journal)
		return nil
	case recoveryEarlyRelaunch:
		if plan.finishServiceStop {
			if err := finishManagedServiceStopForRecovery(root, journal.Request); err != nil {
				return err
			}
		}
	case recoveryRollback:
		if err := rollbackJournal(journal, directory); err != nil {
			return err
		}
	case recoveryRemoveGeneration:
		if err := os.RemoveAll(journal.GenerationDir); err != nil {
			return err
		}
	case recoveryRelaunch, recoveryCheckpoint:
	}
	if plan.action != recoveryEarlyRelaunch && plan.action != recoveryRollback {
		journal.Phase = journalPhaseRollbackRelaunch
		if err := writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal); err != nil {
			return err
		}
	}
	if plan.action == recoveryCheckpoint {
		return nil
	}
	relaunch := journal.Request
	relaunch.ReadyPath = filepath.Join(directory, "recovery-ready.json")
	relaunch.Nonce, _ = randomHex(24)
	if err := launchAndAwaitReady(relaunch, *journal.Previous, nil); err != nil {
		return err
	}
	if hasManagedService(journal.Request) {
		restartService := true
		if plan.action == recoveryEarlyRelaunch {
			_, serviceErr := ReadManagedService(root)
			restartService = !plan.serviceIfMissing || serviceErr != nil
		}
		if restartService {
			if err := launchManagedService(journal.Request, *journal.Previous, nil); err != nil {
				return err
			}
		}
	}
	result := ApplyResult{Schema: SchemaVersion, Product: "Headroom", Status: "rolled_back", Version: journal.Previous.ActiveVersion,
		VersionPath: journal.Previous.VersionPath, RolledBack: true, FailureReason: "An interrupted update was rolled back."}
	if err := writeDurableJSON(filepath.Join(root, "last-apply-result.json"), result); err != nil {
		return err
	}
	if plan.action == recoveryEarlyRelaunch {
		_ = os.RemoveAll(directory)
		return nil
	}
	journal.Phase = journalPhaseRolledBack
	return writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal)
}
