package contract

type journalPhase string

const (
	journalPhasePrepared             journalPhase = "prepared"
	journalPhaseWaitingForExit       journalPhase = "waiting-for-exit"
	journalPhaseCurrentExited        journalPhase = "current-exited"
	journalPhaseStoppingParticipants journalPhase = "stopping-participants"
	journalPhaseParticipantsStopped  journalPhase = "participants-stopped"
	journalPhaseCopying              journalPhase = "copying"
	journalPhaseGenerationReady      journalPhase = "generation-ready"
	journalPhaseBootstrapIntent      journalPhase = "bootstrap-intent"
	journalPhaseBootstrapReady       journalPhase = "bootstrap-ready"
	journalPhaseStateIntent          journalPhase = "state-intent"
	journalPhaseActivated            journalPhase = "activated"
	journalPhaseCandidateLaunched    journalPhase = "candidate-launched"
	journalPhaseComplete             journalPhase = "complete"
	journalPhaseRollingBack          journalPhase = "rolling-back"
	journalPhaseRollbackRelaunch     journalPhase = "rollback-relaunch"
	journalPhaseRolledBack           journalPhase = "rolled-back"
	journalPhaseObsolete             journalPhase = "obsolete"
	journalPhaseEntryIntent          journalPhase = "entry-intent"
)

func writeApplyJournal(path string, journal ApplyJournal) error {
	if err := writeDurableJSON(path, journal); err != nil {
		return err
	}
	if transactionTestHook != nil {
		return transactionTestHook(string(journal.Phase))
	}
	return nil
}
