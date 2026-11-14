package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type reinstallObservation struct {
	root   string
	prior  InstallState
	digest string
	stage  StageResult
}

type reinstallReply struct {
	OK                   bool   `json:"ok"`
	Status               string `json:"status"`
	RequestNonce         string `json:"request_nonce"`
	Version              string `json:"version"`
	TransactionDirectory string `json:"transaction_directory"`
	Nonce                string `json:"nonce"`
}

func coordinateReinstall(ctx context.Context, bridge string, observation reinstallObservation) (InstallResult, error) {
	result := InstallResult{State: &observation.prior}
	nonce, err := randomHex(24)
	if err != nil {
		return result, err
	}
	payload, err := json.Marshal(struct {
		Schema              int            `json:"schema"`
		Product             string         `json:"product"`
		Command             string         `json:"command"`
		InstallRoot         string         `json:"install_root"`
		RequestNonce        string         `json:"request_nonce"`
		PriorStateSHA256    string         `json:"prior_state_sha256"`
		PreparedStage       StageResult    `json:"prepared_stage"`
		AdditionalProcesses []ProcessClaim `json:"additional_processes"`
	}{SchemaVersion, "Headroom", "reinstall", observation.root, nonce, observation.digest, observation.stage, []ProcessClaim{}})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	data, absent, dispatchErr := callDesktop(ctx, bridge, payload)
	if dispatchErr != nil || absent {
		return result, errors.Join(errors.New("reinstall dispatch completion is unconfirmed; staging retained"), dispatchErr)
	}
	var reply reinstallReply
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reply) != nil || ensureEOF(decoder) != nil || reply.RequestNonce != nonce {
		return result, errors.New("reinstall reply is invalid; staging retained")
	}
	if !reply.OK || reply.Status != "accepted" || reply.Version != observation.stage.Version || len(reply.Nonce) != 48 || !isLowerHex(reply.Nonce) {
		return result, errors.New("desktop did not accept reinstall; staging retained")
	}
	request, _, err := readApplyRequest(filepath.Join(reply.TransactionDirectory, TransactionRequestName))
	if err != nil {
		return result, fmt.Errorf("reinstall transaction unavailable: %w", err)
	}
	if request.Nonce != reply.Nonce || request.InstallRoot != observation.root || request.PriorStateSHA256 != observation.digest ||
		request.StageRecord != stageRecordPath(observation.stage) || request.CurrentRole != RoleApplication || request.CandidateRole != RoleApplication ||
		!samePath(request.CurrentExecutable, bridge) {
		return result, errors.New("reinstall transaction does not match dispatched stage and baseline")
	}
	return awaitReinstallOutcome(ctx, observation, request)
}

func awaitReinstallOutcome(ctx context.Context, observation reinstallObservation, request ApplyRequest) (InstallResult, error) {
	result := InstallResult{State: &observation.prior}
	for {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("reinstall completion unconfirmed: %w", err)
		}
		lock, err := acquireInstallLock(observation.root, 100*time.Millisecond)
		if err == nil {
			settled, observed, outcomeErr := settledReinstallOutcome(observation, request)
			closeErr := lock.Close()
			if settled || outcomeErr != nil || closeErr != nil {
				return observed, errors.Join(outcomeErr, closeErr)
			}
			result = observed
		}
		if err = observePause(ctx); err != nil {
			return result, fmt.Errorf("reinstall completion unconfirmed: %w", err)
		}
	}
}

func settledReinstallOutcome(observation reinstallObservation, request ApplyRequest) (bool, InstallResult, error) {
	result := InstallResult{State: &observation.prior}
	state, err := readTrustedState(observation.root, false)
	if err != nil {
		return false, result, err
	}
	result.State = state
	data, err := readBoundedFile(filepath.Join(observation.root, "last-apply-result.json"), maxManifestBytes)
	if err != nil {
		return false, result, nil
	}
	var outcome ApplyResult
	if json.Unmarshal(data, &outcome) != nil || outcome.Schema != SchemaVersion || outcome.Product != "Headroom" || outcome.Nonce != request.Nonce {
		return false, result, nil
	}
	journal, journalErr := readJournal(filepath.Join(filepath.Dir(request.CommitPath), TransactionJournalName))
	if journalErr == nil && (!applyRequestsEqual(journal.Request, request) ||
		outcome.Status == "applied" && journal.Phase != journalPhaseComplete || outcome.Status == "rolled_back" && journal.Phase != journalPhaseRolledBack) {
		return false, result, nil
	}
	if state == nil || outcome.Version != state.ActiveVersion || outcome.VersionPath != state.VersionPath {
		return false, result, nil
	}
	switch outcome.Status {
	case "applied":
		if outcome.RolledBack || state.VersionPath == observation.prior.VersionPath || state.ActiveVersion != observation.stage.Version ||
			state.ManifestSHA256 != observation.stage.ManifestSHA256 || state.PackageAsset != observation.stage.PackageAsset {
			return false, result, errors.New("reinstall result generation does not match its stage")
		}
		if journalErr == nil && (journal.Candidate == nil || !statesEqual(*journal.Candidate, *state)) {
			return false, result, errors.New("reinstall result differs from transaction candidate")
		}
		result.Activation = &ActivationResult{Status: "ready", Reason: "coordinated_reinstall", VersionPath: state.VersionPath}
		return true, result, nil
	case "rolled_back", "recovery_required":
		return true, result, fmt.Errorf("reinstall %s: %s", outcome.Status, outcome.FailureReason)
	default:
		return false, result, errors.New("reinstall outcome status is invalid")
	}
}
