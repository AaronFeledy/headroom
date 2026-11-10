package pairing

type pairingState string

const (
	StatePending pairingState = "pending"
	StateActive  pairingState = "active"
)

type progressPhase string

const (
	progressPhasePrepared       progressPhase = "prepared"
	progressPhasePeerCommitting progressPhase = "peer_committing"
	progressPhasePeerApplied    progressPhase = "peer_applied"
	progressPhaseComplete       progressPhase = "complete"
)
