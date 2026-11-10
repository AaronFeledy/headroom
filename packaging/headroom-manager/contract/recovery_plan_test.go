package contract

import "testing"

func TestApplyRecoveryDecisionTable(t *testing.T) {
	// Columns are neither, prior without a generation, prior with a generation,
	// candidate, and both. These preserve the original recovery branch precedence.
	tests := []struct {
		phase   journalPhase
		actions [5]recoveryAction
		stops   bool
	}{
		{journalPhasePrepared, [5]recoveryAction{recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard}, false},
		{journalPhaseWaitingForExit, [5]recoveryAction{recoveryObsolete, recoveryDiscard, recoveryDiscard, recoveryRollback, recoveryDiscard}, false},
		{journalPhaseCurrentExited, [5]recoveryAction{recoveryObsolete, recoveryEarlyRelaunch, recoveryEarlyRelaunch, recoveryRollback, recoveryEarlyRelaunch}, false},
		{journalPhaseStoppingParticipants, [5]recoveryAction{recoveryObsolete, recoveryEarlyRelaunch, recoveryEarlyRelaunch, recoveryRollback, recoveryEarlyRelaunch}, false},
		{journalPhaseParticipantsStopped, [5]recoveryAction{recoveryObsolete, recoveryEarlyRelaunch, recoveryEarlyRelaunch, recoveryRollback, recoveryEarlyRelaunch}, false},
		{journalPhaseCopying, [5]recoveryAction{recoveryObsolete, recoveryEarlyRelaunch, recoveryEarlyRelaunch, recoveryRollback, recoveryEarlyRelaunch}, false},
		{journalPhaseGenerationReady, [5]recoveryAction{recoveryObsolete, recoveryCheckpoint, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, false},
		{journalPhaseBootstrapIntent, [5]recoveryAction{recoveryObsolete, recoveryRollback, recoveryRollback, recoveryRollback, recoveryRollback}, false},
		{journalPhaseBootstrapReady, [5]recoveryAction{recoveryObsolete, recoveryRollback, recoveryRollback, recoveryRollback, recoveryRollback}, false},
		{journalPhaseStateIntent, [5]recoveryAction{recoveryObsolete, recoveryRollback, recoveryRollback, recoveryRollback, recoveryRollback}, false},
		{journalPhaseActivated, [5]recoveryAction{recoveryObsolete, recoveryCheckpoint, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, true},
		{journalPhaseCandidateLaunched, [5]recoveryAction{recoveryObsolete, recoveryCheckpoint, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, true},
		{journalPhaseComplete, [5]recoveryAction{recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard}, false},
		{journalPhaseRollingBack, [5]recoveryAction{recoveryObsolete, recoveryRelaunch, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, true},
		{journalPhaseRollbackRelaunch, [5]recoveryAction{recoveryObsolete, recoveryRelaunch, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, true},
		{journalPhaseRolledBack, [5]recoveryAction{recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard, recoveryDiscard}, false},
		{journalPhaseObsolete, [5]recoveryAction{recoveryObsolete, recoveryCheckpoint, recoveryRemoveGeneration, recoveryRollback, recoveryRollback}, false},
	}
	for _, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			for flags := 0; flags < 32; flags++ {
				journal := ApplyJournal{Phase: test.phase}
				candidatePID, servicePID := flags&1 != 0, flags&2 != 0
				supervisor, managed, generation := flags&4 != 0, flags&8 != 0, flags&16 != 0
				if candidatePID {
					journal.CandidatePID = 1
				}
				if servicePID {
					journal.ServicePID = 2
				}
				if supervisor {
					journal.Request.ManagedServiceSupervisor = "fixture-supervisor"
				}
				if managed {
					journal.Request.AdditionalProcesses = []ProcessClaim{{Role: RoleManagedServer}}
				}
				if generation {
					journal.GenerationDir = "fixture-generation"
				}
				for _, match := range []recoveryStateMatch{{}, {prior: true}, {candidate: true}, {prior: true, candidate: true}} {
					column := 0
					switch {
					case match.prior && match.candidate:
						column = 4
					case match.candidate:
						column = 3
					case match.prior && generation:
						column = 2
					case match.prior:
						column = 1
					}
					want := applyRecoveryPlan{action: test.actions[column], stopCandidate: test.stops && candidatePID,
						recordApplied: test.phase == journalPhaseComplete && match.candidate}
					if test.stops {
						switch {
						case supervisor && managed:
							want.stopService = recoveryServiceStopAndClearSupervisor
						case supervisor && servicePID:
							want.stopService = recoveryServiceStopSupervisor
						case servicePID:
							want.stopService = recoveryServiceStopProcess
						}
					}
					if want.action == recoveryEarlyRelaunch {
						want.finishServiceStop = test.phase != journalPhaseCurrentExited && managed
						want.serviceIfMissing = test.phase == journalPhaseCurrentExited
					}
					if got := planApplyRecovery(journal, match); got != want {
						t.Errorf("flags=%05b match=%+v: plan=%+v, want=%+v", flags, match, got, want)
					}
				}
			}
		})
	}
}
