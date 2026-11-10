package contract

type recoveryAction uint8

const (
	recoveryDiscard recoveryAction = iota
	recoveryEarlyRelaunch
	recoveryRollback
	recoveryRemoveGeneration
	recoveryRelaunch
	recoveryObsolete
	recoveryCheckpoint
)

type recoveryServiceStop uint8

const (
	recoveryServiceKeep recoveryServiceStop = iota
	recoveryServiceStopProcess
	recoveryServiceStopSupervisor
	recoveryServiceStopAndClearSupervisor
)

type recoveryStateMatch struct {
	prior     bool
	candidate bool
}

type applyRecoveryPlan struct {
	action            recoveryAction
	stopCandidate     bool
	stopService       recoveryServiceStop
	finishServiceStop bool
	serviceIfMissing  bool
	recordApplied     bool
}

func planApplyRecovery(journal ApplyJournal, match recoveryStateMatch) applyRecoveryPlan {
	plan := applyRecoveryPlan{action: recoveryCheckpoint}
	switch journal.Phase {
	case journalPhasePrepared, journalPhaseComplete, journalPhaseRolledBack:
		plan.action = recoveryDiscard
		plan.recordApplied = journal.Phase == journalPhaseComplete && match.candidate
		return plan
	case journalPhaseActivated, journalPhaseCandidateLaunched, journalPhaseRollingBack, journalPhaseRollbackRelaunch:
		plan.stopCandidate = journal.CandidatePID > 0
		switch {
		case journal.Request.ManagedServiceSupervisor != "" && hasManagedService(journal.Request):
			plan.stopService = recoveryServiceStopAndClearSupervisor
		case journal.ServicePID > 0 && journal.Request.ManagedServiceSupervisor != "":
			plan.stopService = recoveryServiceStopSupervisor
		case journal.ServicePID > 0:
			plan.stopService = recoveryServiceStopProcess
		}
	}
	if match.prior {
		switch journal.Phase {
		case journalPhaseWaitingForExit:
			plan.action = recoveryDiscard
			return plan
		case journalPhaseCurrentExited, journalPhaseStoppingParticipants, journalPhaseParticipantsStopped, journalPhaseCopying:
			plan.action = recoveryEarlyRelaunch
			plan.finishServiceStop = journal.Phase != journalPhaseCurrentExited && hasManagedService(journal.Request)
			plan.serviceIfMissing = journal.Phase == journalPhaseCurrentExited
			return plan
		}
	}
	if match.candidate {
		plan.action = recoveryRollback
		return plan
	}
	if !match.prior {
		plan.action = recoveryObsolete
		return plan
	}
	switch journal.Phase {
	case journalPhaseBootstrapIntent, journalPhaseBootstrapReady, journalPhaseStateIntent:
		plan.action = recoveryRollback
	default:
		switch {
		case journal.GenerationDir != "":
			plan.action = recoveryRemoveGeneration
		case journal.Phase == journalPhaseRollingBack || journal.Phase == journalPhaseRollbackRelaunch:
			plan.action = recoveryRelaunch
		}
	}
	return plan
}
