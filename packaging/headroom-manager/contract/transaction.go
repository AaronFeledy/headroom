package contract

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const TransactionRequestName = "apply-request.json"
const TransactionJournalName = "apply-journal.json"
const InstallJournalName = "install-journal.json"
const EntryMigrationJournalName = "entry-migration.json"

var transactionTestHook func(string) error

type ApplyRequest struct {
	Schema                         int            `json:"schema"`
	Product                        string         `json:"product"`
	InstallRoot                    string         `json:"install_root"`
	EntryPath                      string         `json:"entry_path"`
	StageRecord                    string         `json:"stage_record"`
	CurrentPID                     int            `json:"current_pid"`
	CurrentExecutable              string         `json:"current_executable"`
	CurrentProcessToken            string         `json:"current_process_token"`
	CurrentRole                    string         `json:"current_role,omitempty"`
	CandidateRole                  string         `json:"candidate_role,omitempty"`
	AdditionalProcesses            []ProcessClaim `json:"additional_processes,omitempty"`
	ManagedServiceArguments        []string       `json:"managed_service_arguments,omitempty"`
	ManagedServiceEnvironment      []string       `json:"managed_service_environment,omitempty"`
	ManagedServiceWorkingDirectory string         `json:"managed_service_working_directory,omitempty"`
	ManagedServiceSupervisor       string         `json:"managed_service_supervisor,omitempty"`
	OwnedChildPID                  int            `json:"owned_child_pid,omitempty"`
	OwnedChildExecutable           string         `json:"owned_child_executable,omitempty"`
	OwnedChildToken                string         `json:"owned_child_token,omitempty"`
	PriorStateSHA256               string         `json:"prior_state_sha256"`
	RelaunchArguments              []string       `json:"relaunch_arguments"`
	AcknowledgementPath            string         `json:"acknowledgement_path"`
	CommitPath                     string         `json:"commit_path"`
	ReadyPath                      string         `json:"ready_path"`
	Nonce                          string         `json:"nonce"`
	WaitTimeoutMS                  int            `json:"wait_timeout_ms"`
	CommitTimeoutMS                int            `json:"commit_timeout_ms"`
	ReadyTimeoutMS                 int            `json:"ready_timeout_ms"`
}

type ProcessClaim struct {
	Role         string `json:"role"`
	PID          int    `json:"pid"`
	Executable   string `json:"executable"`
	ProcessToken string `json:"process_token,omitempty"`
}

type ApplyJournal struct {
	Schema         int           `json:"schema"`
	Product        string        `json:"product"`
	Phase          journalPhase  `json:"phase"`
	Request        ApplyRequest  `json:"request"`
	Previous       *InstallState `json:"previous,omitempty"`
	Candidate      *InstallState `json:"candidate,omitempty"`
	GenerationDir  string        `json:"generation_dir,omitempty"`
	CandidatePID   int           `json:"candidate_pid,omitempty"`
	CandidateExe   string        `json:"candidate_executable,omitempty"`
	CandidateToken string        `json:"candidate_process_token,omitempty"`
	ServicePID     int           `json:"managed_service_pid,omitempty"`
	ServiceExe     string        `json:"managed_service_executable,omitempty"`
	ServiceToken   string        `json:"managed_service_process_token,omitempty"`
	Error          string        `json:"error,omitempty"`
}

type InstallJournal struct {
	Schema            int          `json:"schema"`
	Product           string       `json:"product"`
	Phase             journalPhase `json:"phase"`
	InstallRoot       string       `json:"install_root"`
	EntryPath         string       `json:"entry_path"`
	CLIEntryPath      string       `json:"cli_entry_path,omitempty"`
	GenerationDir     string       `json:"generation_dir,omitempty"`
	Candidate         InstallState `json:"candidate"`
	PriorStateExisted bool         `json:"prior_state_existed"`
	PriorStateSHA256  string       `json:"prior_state_sha256,omitempty"`
	Supersedes        []string     `json:"supersedes,omitempty"`
}

type EntryMigrationJournal struct {
	Schema       int          `json:"schema"`
	Product      string       `json:"product"`
	Phase        journalPhase `json:"phase"`
	InstallRoot  string       `json:"install_root"`
	EntryPath    string       `json:"entry_path"`
	CLIEntryPath string       `json:"cli_entry_path"`
	Previous     InstallState `json:"previous"`
	Candidate    InstallState `json:"candidate"`
}

type PreparedApply struct {
	Schema               int    `json:"schema"`
	Product              string `json:"product"`
	TransactionDirectory string `json:"transaction_directory"`
	RequestPath          string `json:"request_path"`
	ManagerPath          string `json:"manager_path"`
	AcknowledgementPath  string `json:"acknowledgement_path"`
	CommitPath           string `json:"commit_path"`
	Nonce                string `json:"nonce"`
}

type ApplyResult struct {
	Schema        int    `json:"schema"`
	Product       string `json:"product"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	VersionPath   string `json:"version_path"`
	RolledBack    bool   `json:"rolled_back"`
	FailureReason string `json:"failure_reason,omitempty"`
}

func PrepareApply(manager string, request ApplyRequest) (PreparedApply, error) {
	root, err := NormalizeInstallRoot(request.InstallRoot)
	if err != nil {
		return PreparedApply{}, err
	}
	request.InstallRoot = root
	request.CurrentRole = normalizedProcessRole(request.CurrentRole)
	request.CandidateRole = normalizedProcessRole(request.CandidateRole)
	if !validProcessRole(request.CurrentRole) || !validProcessRole(request.CandidateRole) {
		return PreparedApply{}, errors.New("apply process role is invalid")
	}
	request.EntryPath, err = normalizeAbsolutePath(request.EntryPath, "entry path")
	if err != nil {
		return PreparedApply{}, err
	}
	if err = validateInstallTargets(root, request.EntryPath); err != nil {
		return PreparedApply{}, err
	}
	request.CurrentExecutable, err = normalizeAbsolutePath(request.CurrentExecutable, "current executable")
	if err != nil {
		return PreparedApply{}, err
	}
	if request.CurrentPID <= 0 || len(request.RelaunchArguments) > 32 {
		return PreparedApply{}, errors.New("apply process identity or arguments are invalid")
	}
	for _, argument := range request.RelaunchArguments {
		if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return PreparedApply{}, errors.New("apply relaunch argument is invalid")
		}
	}
	lock, err := acquireInstallLock(root, 10*time.Second)
	if err != nil {
		return PreparedApply{}, err
	}
	defer lock.Close()
	if err = rejectIncompleteTransactions(root); err != nil {
		return PreparedApply{}, err
	}
	stage, manifest, err := LoadVerifiedStage(root, request.StageRecord)
	if err != nil {
		return PreparedApply{}, err
	}
	inspection := InspectInstall(root)
	comparison, compareErr := CompareVersions(manifest.Version, inspection.Version)
	if compareErr != nil || comparison < 0 || stage.Version != manifest.Version || stage.Platform != inspection.Platform ||
		stage.Architecture != inspection.Architecture || stage.PackageKind != inspection.PackageKind {
		return PreparedApply{}, errors.New("staged package does not match the installed platform, profile, or version direction")
	}
	receipt, receiptErr := readStateUnchecked(root)
	if receiptErr != nil {
		return PreparedApply{}, errors.New("installed state is not readable")
	}
	if request.CurrentRole == RoleCLI && (inspection.CLIEntryPath == "" || !samePath(request.EntryPath, inspection.CLIEntryPath)) {
		return PreparedApply{}, errors.New("CLI apply entry does not match the trusted install receipt")
	}
	if request.CurrentRole == RoleApplication && receipt.ApplicationEntryPath != "" && !samePath(request.EntryPath, receipt.ApplicationEntryPath) {
		return PreparedApply{}, errors.New("application apply entry does not match the trusted install receipt")
	}
	executable, _, activeErr := ActiveExecutableForRole(root, request.CurrentRole)
	if activeErr != nil || !inspection.TrustedIdentity || !samePath(executable, request.CurrentExecutable) {
		return PreparedApply{}, errors.New("running Headroom installation is not a launchable rollback baseline")
	}
	request.CurrentProcessToken, err = captureProcessToken(request.CurrentPID, request.CurrentExecutable)
	if err != nil {
		return PreparedApply{}, err
	}
	if len(request.ManagedServiceArguments) != 0 || len(request.ManagedServiceEnvironment) != 0 || request.ManagedServiceWorkingDirectory != "" || request.ManagedServiceSupervisor != "" {
		return PreparedApply{}, errors.New("managed service restart configuration is manager-owned")
	}
	managedRecord, managedErr := ReadManagedService(root)
	if managedErr == nil {
		found, launcherFound := false, false
		for _, claim := range request.AdditionalProcesses {
			if claim.Role == RoleManagedServer {
				found = true
			}
			if claim.Role == RoleManagedLauncher {
				launcherFound = true
			}
		}
		if !found {
			request.AdditionalProcesses = append(request.AdditionalProcesses, ProcessClaim{Role: RoleManagedServer, PID: managedRecord.PID, Executable: managedRecord.Executable})
		}
		if managedRecord.LauncherPID > 0 && !launcherFound {
			request.AdditionalProcesses = append(request.AdditionalProcesses, ProcessClaim{Role: RoleManagedLauncher, PID: managedRecord.LauncherPID, Executable: managedRecord.LauncherExecutable})
		}
	} else if !errors.Is(managedErr, os.ErrNotExist) {
		return PreparedApply{}, fmt.Errorf("managed service receipt is invalid: %w", managedErr)
	}
	if len(request.AdditionalProcesses) > 4 {
		return PreparedApply{}, errors.New("too many additional update participants")
	}
	seenPID := map[int]bool{request.CurrentPID: true}
	seenRole := map[string]bool{request.CurrentRole: true}
	if request.OwnedChildPID > 0 {
		seenPID[request.OwnedChildPID] = true
		seenRole[RoleServer] = true
	}
	for index := range request.AdditionalProcesses {
		claim := &request.AdditionalProcesses[index]
		claim.Role = normalizedProcessRole(claim.Role)
		if !validParticipantRole(claim.Role) || claim.PID <= 0 || claim.ProcessToken != "" || seenPID[claim.PID] || seenRole[claim.Role] {
			return PreparedApply{}, errors.New("additional update participant is invalid")
		}
		claim.Executable, err = normalizeAbsolutePath(claim.Executable, "participant executable")
		if err != nil {
			return PreparedApply{}, err
		}
		expected, _, resolveErr := ActiveExecutableForRole(root, claim.Role)
		if resolveErr != nil || !samePath(expected, claim.Executable) {
			return PreparedApply{}, errors.New("additional update participant is not an active installed component")
		}
		claim.ProcessToken, err = captureProcessToken(claim.PID, claim.Executable)
		if err != nil {
			return PreparedApply{}, err
		}
		seenPID[claim.PID], seenRole[claim.Role] = true, true
		if claim.Role == RoleManagedServer {
			record, serviceErr := ReadManagedService(root)
			if serviceErr != nil || record.PID != claim.PID || record.ProcessToken != claim.ProcessToken || !samePath(record.Executable, claim.Executable) {
				return PreparedApply{}, errors.New("managed server participant does not match its trusted receipt")
			}
			request.ManagedServiceArguments = append([]string(nil), record.Arguments...)
			request.ManagedServiceEnvironment = append([]string(nil), record.Environment...)
			request.ManagedServiceWorkingDirectory = record.WorkingDirectory
			request.ManagedServiceSupervisor = record.Supervisor
		} else if claim.Role == RoleManagedLauncher {
			record, serviceErr := ReadManagedService(root)
			if serviceErr != nil || record.LauncherPID != claim.PID || record.LauncherToken != claim.ProcessToken || !samePath(record.LauncherExecutable, claim.Executable) {
				return PreparedApply{}, errors.New("managed service launcher does not match its trusted receipt")
			}
		}
	}
	if len(request.ManagedServiceArguments) != 0 && !seenRole[RoleManagedServer] {
		return PreparedApply{}, errors.New("managed service arguments have no participant")
	}
	if request.OwnedChildPID > 0 {
		request.OwnedChildExecutable, err = normalizeAbsolutePath(request.OwnedChildExecutable, "owned child executable")
		if err != nil {
			return PreparedApply{}, err
		}
		manifestFile, openErr := os.Open(filepath.Join(root, filepath.FromSlash(inspection.VersionPath), PackageManifestName))
		if openErr != nil {
			return PreparedApply{}, openErr
		}
		manifest, decodeErr := DecodePackageManifest(manifestFile)
		manifestFile.Close()
		if decodeErr != nil {
			return PreparedApply{}, decodeErr
		}
		expectedChild := installedComponentPath(root, inspection.VersionPath, manifest.Components.Server.Path)
		if !samePath(request.OwnedChildExecutable, expectedChild) {
			return PreparedApply{}, errors.New("owned child is not the active bundled usage server")
		}
		request.OwnedChildToken, err = captureProcessToken(request.OwnedChildPID, request.OwnedChildExecutable)
		if err != nil {
			return PreparedApply{}, err
		}
	} else if request.OwnedChildExecutable != "" {
		return PreparedApply{}, errors.New("owned child identity is incomplete")
	}
	request.PriorStateSHA256, err = digestFile(filepath.Join(root, StateName))
	if err != nil {
		return PreparedApply{}, err
	}
	if stage.Version == "" {
		return PreparedApply{}, errors.New("verified stage has no version")
	}
	transactions, err := ensureOwnedDirectory(root, "transactions", 0o700)
	if err != nil {
		return PreparedApply{}, err
	}
	token, err := randomHex(16)
	if err != nil {
		return PreparedApply{}, err
	}
	directory := filepath.Join(transactions, "apply-"+token)
	if err = os.Mkdir(directory, 0o700); err != nil {
		return PreparedApply{}, err
	}
	if err = securePrivateDirectory(directory, true); err != nil {
		_ = os.RemoveAll(directory)
		return PreparedApply{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(directory)
		}
	}()
	ext := ""
	if stage.Platform == "windows" {
		ext = ".exe"
	}
	managerCopy := filepath.Join(directory, "headroom-apply"+ext)
	if err = copyFile(manager, managerCopy, 0o700); err != nil {
		return PreparedApply{}, err
	}
	request.Schema, request.Product = SchemaVersion, "Headroom"
	request.StageRecord = filepath.Clean(request.StageRecord)
	request.AcknowledgementPath = filepath.Join(directory, "accepted.json")
	request.CommitPath = filepath.Join(directory, "commit.json")
	request.ReadyPath = filepath.Join(directory, "ready.json")
	request.Nonce, err = randomHex(24)
	if err != nil {
		return PreparedApply{}, err
	}
	if request.WaitTimeoutMS <= 0 || request.WaitTimeoutMS > 120000 {
		request.WaitTimeoutMS = 30000
	}
	if request.CommitTimeoutMS <= 0 || request.CommitTimeoutMS > 30000 {
		request.CommitTimeoutMS = 15000
	}
	if request.ReadyTimeoutMS <= 0 || request.ReadyTimeoutMS > 120000 {
		request.ReadyTimeoutMS = 30000
	}
	requestPath := filepath.Join(directory, TransactionRequestName)
	if err = writeDurableJSON(requestPath, request); err != nil {
		return PreparedApply{}, err
	}
	journal := ApplyJournal{Schema: SchemaVersion, Product: "Headroom", Phase: journalPhasePrepared, Request: request}
	if err = writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal); err != nil {
		return PreparedApply{}, err
	}
	failed = false
	return PreparedApply{Schema: SchemaVersion, Product: "Headroom", TransactionDirectory: directory,
		RequestPath: requestPath, ManagerPath: managerCopy, AcknowledgementPath: request.AcknowledgementPath,
		CommitPath: request.CommitPath, Nonce: request.Nonce}, nil
}

func rejectIncompleteTransactions(root string) error {
	entries, err := os.ReadDir(filepath.Join(root, "transactions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "apply-") {
			continue
		}
		journal, readErr := readJournal(filepath.Join(root, "transactions", entry.Name(), TransactionJournalName))
		if readErr == nil && journal.Schema == SchemaVersion && journal.Product == "Headroom" &&
			journal.Phase != journalPhaseComplete && journal.Phase != journalPhaseRolledBack && journal.Phase != journalPhaseObsolete {
			return errors.New("another Headroom apply transaction must be recovered before preparing an update")
		}
	}
	return nil
}

func supersedeUnrecoverableTransactions(root string) ([]string, error) {
	current, err := readStateUnchecked(root)
	if err != nil {
		return nil, errors.New("unrecoverable transaction has no valid installed state")
	}
	entries, err := os.ReadDir(filepath.Join(root, "transactions"))
	if err != nil {
		return nil, err
	}
	var superseded []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(root, "transactions", entry.Name())
		if strings.HasPrefix(entry.Name(), "apply-") {
			journal, readErr := readJournal(filepath.Join(directory, TransactionJournalName))
			if readErr != nil || validateApplyJournal(journal, root, directory) != nil || journal.Phase == journalPhaseComplete || journal.Phase == journalPhaseRolledBack || journal.Phase == journalPhaseObsolete {
				continue
			}
			ownsState := journal.Previous != nil && statesEqual(current, *journal.Previous) || journal.Candidate != nil && statesEqual(current, *journal.Candidate)
			if !ownsState {
				continue
			}
			if journal.CandidatePID > 0 {
				if err = stopRecordedProcess(journal.CandidatePID, journal.CandidateExe, journal.CandidateToken, 5*time.Second); err != nil {
					return nil, fmt.Errorf("cannot supersede running update candidate: %w", err)
				}
			}
			superseded = append(superseded, directory)
			continue
		}
		if strings.HasPrefix(entry.Name(), "install-") {
			journal, readErr := readInstallJournal(filepath.Join(directory, InstallJournalName), root, directory)
			if readErr != nil || journal.Phase == journalPhaseComplete || journal.Phase == journalPhaseRolledBack || journal.Phase == journalPhaseObsolete {
				continue
			}
			if !statesEqual(current, journal.Candidate) {
				if prior, priorErr := digestFile(filepath.Join(root, StateName)); !journal.PriorStateExisted || priorErr != nil || prior != journal.PriorStateSHA256 {
					continue
				}
			}
			superseded = append(superseded, directory)
		}
	}
	if len(superseded) == 0 {
		return nil, errors.New("recovery failure is not an owned transaction that a verified reinstall can supersede")
	}
	return superseded, nil
}

func WaitForApplyAcknowledgement(prepared PreparedApply, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := readBoundedFile(prepared.AcknowledgementPath, 4096)
		if err == nil {
			var acknowledgement struct {
				Schema   int    `json:"schema"`
				Product  string `json:"product"`
				Accepted bool   `json:"accepted"`
				Nonce    string `json:"nonce"`
			}
			if json.Unmarshal(data, &acknowledgement) == nil && acknowledgement.Schema == SchemaVersion && acknowledgement.Product == "Headroom" && acknowledgement.Accepted && acknowledgement.Nonce == prepared.Nonce {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("transaction manager did not acknowledge the apply request")
}

func CommitPreparedApply(prepared PreparedApply) error {
	if prepared.Schema != SchemaVersion || prepared.Product != "Headroom" || len(prepared.Nonce) != 48 || !isLowerHex(prepared.Nonce) {
		return errors.New("prepared apply identity is invalid")
	}
	directory := filepath.Clean(prepared.TransactionDirectory)
	if !samePath(prepared.CommitPath, filepath.Join(directory, "commit.json")) || !samePath(prepared.RequestPath, filepath.Join(directory, TransactionRequestName)) {
		return errors.New("prepared apply paths are invalid")
	}
	request, _, err := readApplyRequest(prepared.RequestPath)
	if err != nil || request.Nonce != prepared.Nonce || !samePath(request.CommitPath, prepared.CommitPath) || !samePath(request.AcknowledgementPath, prepared.AcknowledgementPath) {
		return errors.New("prepared apply request is invalid")
	}
	return writeDurableJSON(prepared.CommitPath, map[string]any{"schema": SchemaVersion, "product": "Headroom", "commit": true, "nonce": prepared.Nonce})
}

func waitForApplyCommit(request ApplyRequest, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := readBoundedFile(request.CommitPath, 4096)
		if err == nil {
			var commit struct {
				Schema  int    `json:"schema"`
				Product string `json:"product"`
				Commit  bool   `json:"commit"`
				Nonce   string `json:"nonce"`
			}
			if json.Unmarshal(data, &commit) == nil && commit.Schema == SchemaVersion && commit.Product == "Headroom" && commit.Commit && commit.Nonce == request.Nonce {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("apply authorization was not committed")
}

func LoadVerifiedStage(installRoot, recordPath string) (StageResult, PackageManifest, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	recordPath, err = normalizeAbsolutePath(recordPath, "stage record")
	if err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	staging, err := ensureOwnedDirectory(root, "staging", 0o700)
	if err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	if !isDirectOwnedRecord(staging, recordPath) {
		return StageResult{}, PackageManifest{}, errors.New("verified stage record is outside the owned staging root")
	}
	data, err := readBoundedFile(recordPath, maxManifestBytes)
	if err != nil {
		return StageResult{}, PackageManifest{}, errors.New("verified stage record is unavailable or oversized")
	}
	var stage StageResult
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&stage) != nil || ensureEOF(decoder) != nil || stage.Schema != SchemaVersion || stage.Product != "Headroom" {
		return StageResult{}, PackageManifest{}, errors.New("verified stage record is invalid")
	}
	packageRoot, err := normalizeAbsolutePath(stage.PackageRoot, "staged package root")
	if err != nil || filepath.Dir(filepath.Dir(packageRoot)) != filepath.Dir(recordPath) {
		return StageResult{}, PackageManifest{}, errors.New("staged package root does not match its record")
	}
	if err = validateInstallTargets(packageRoot, ""); err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	manifestFile, err := os.Open(filepath.Join(packageRoot, PackageManifestName))
	if err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	manifest, decodeErr := DecodePackageManifest(manifestFile)
	manifestFile.Close()
	if decodeErr != nil {
		return StageResult{}, PackageManifest{}, decodeErr
	}
	hash, err := digestFile(filepath.Join(packageRoot, PackageManifestName))
	if err != nil || hash != stage.ManifestSHA256 || manifest.Version != stage.Version || manifest.Platform != stage.Platform ||
		manifest.Architecture != stage.Architecture || manifest.AssetName != stage.PackageAsset || manifest.PackageKind != stage.PackageKind {
		return StageResult{}, PackageManifest{}, errors.New("verified stage identity changed")
	}
	if err = VerifyTree(packageRoot, manifest); err != nil {
		return StageResult{}, PackageManifest{}, err
	}
	stage.PackageRoot = packageRoot
	return stage, manifest, nil
}

func ApplyPrepared(requestPath string) (result ApplyResult, returned error) {
	var previous *InstallState
	var journal ApplyJournal
	var childWatch processWatch
	type participantWatch struct {
		role  string
		watch processWatch
	}
	var additionalWatches []participantWatch
	appExited := false
	childExited := false
	complete := false
	recoverFailure := func() {
		if !appExited || complete || returned == nil || previous == nil {
			return
		}
		directory := filepath.Dir(requestPath)
		originalFailure := returned
		originalPhase := journal.Phase
		var cleanupErr error
		journal.Error, journal.Phase = boundedError(originalFailure), journalPhaseRollingBack
		if err := writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal); err != nil {
			cleanupErr = fmt.Errorf("record rollback intent: %w", err)
		}
		if journal.CandidatePID > 0 {
			if err := stopRecordedProcess(journal.CandidatePID, journal.CandidateExe, journal.CandidateToken, 5*time.Second); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop candidate: %w", err))
			}
		}
		if journal.ServicePID > 0 {
			var err error
			if journal.Request.ManagedServiceSupervisor != "" {
				err = stopManagedServiceSupervisor(journal.Request.ManagedServiceSupervisor)
			} else {
				err = stopRecordedProcess(journal.ServicePID, journal.ServiceExe, journal.ServiceToken, 5*time.Second)
			}
			if err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop managed service candidate: %w", err))
			}
		}
		if childWatch != nil && !childExited {
			if err := childWatch.KillWait(5 * time.Second); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop owned server: %w", err))
			}
		}
		for _, participant := range additionalWatches {
			if participant.role == RoleManagedServer && journal.Request.ManagedServiceSupervisor != "" {
				continue
			}
			if err := participant.watch.KillWait(5 * time.Second); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop update participant: %w", err))
			}
		}
		if cleanupErr == nil {
			if journal.Candidate != nil && (originalPhase == journalPhaseBootstrapIntent || originalPhase == journalPhaseBootstrapReady || originalPhase == journalPhaseStateIntent || originalPhase == journalPhaseActivated || originalPhase == journalPhaseCandidateLaunched || originalPhase == journalPhaseComplete) {
				cleanupErr = rollbackJournal(journal, directory)
				if cleanupErr == nil {
					journal.Phase = journalPhaseRollbackRelaunch
				}
			} else if journal.GenerationDir != "" {
				cleanupErr = os.RemoveAll(journal.GenerationDir)
				if cleanupErr == nil && transactionTestHook != nil {
					cleanupErr = transactionTestHook("rollback-generation-only-removed")
				}
			}
		}
		if cleanupErr == nil {
			current, stateErr := readStateUnchecked(journal.Request.InstallRoot)
			if stateErr != nil || !statesEqual(current, *previous) {
				cleanupErr = errors.New("rollback state could not be verified")
			} else if _, _, activeErr := ActiveExecutableForRole(journal.Request.InstallRoot, journal.Request.CandidateRole); activeErr != nil {
				cleanupErr = fmt.Errorf("rollback application could not be verified: %w", activeErr)
			} else if verifyErr := verifyBootstrapRestoreWithCLI(journal.Request.InstallRoot, journal.Request.EntryPath, previous.CLIEntryPath, directory); verifyErr != nil {
				cleanupErr = verifyErr
			}
		}
		if cleanupErr == nil && journal.Phase != journalPhaseRollbackRelaunch {
			journal.Phase = journalPhaseRollbackRelaunch
			cleanupErr = writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal)
		}
		if cleanupErr == nil {
			result = ApplyResult{Schema: SchemaVersion, Product: "Headroom", Status: "rolled_back", Version: previous.ActiveVersion,
				VersionPath: previous.VersionPath, RolledBack: true, FailureReason: boundedError(originalFailure)}
			relaunch := journal.Request
			relaunch.ReadyPath = filepath.Join(directory, "rollback-ready.json")
			relaunch.Nonce, _ = randomHex(24)
			cleanupErr = relaunchRollbackPrior(relaunch, *previous)
		}
		if cleanupErr != nil {
			result = ApplyResult{Schema: SchemaVersion, Product: "Headroom", Status: "recovery_required", Version: previous.ActiveVersion,
				VersionPath: previous.VersionPath, FailureReason: boundedError(errors.Join(originalFailure, cleanupErr))}
			if current, stateErr := readStateUnchecked(journal.Request.InstallRoot); stateErr == nil && validVersionPath(current) {
				result.Version, result.VersionPath = current.ActiveVersion, current.VersionPath
			}
			returned = errors.Join(originalFailure, cleanupErr)
		}
		if persistErr := writeDurableJSON(filepath.Join(journal.Request.InstallRoot, "last-apply-result.json"), result); persistErr != nil {
			returned = errors.Join(returned, fmt.Errorf("persist apply outcome: %w", persistErr))
		} else if cleanupErr == nil {
			journal.Phase = journalPhaseRolledBack
			if err := writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal); err != nil {
				returned = errors.Join(returned, err)
			}
		}
	}
	request, journalPath, err := readApplyRequest(requestPath)
	if err != nil {
		return result, err
	}
	lock, err := acquireInstallLock(request.InstallRoot, 10*time.Second)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	preparedJournal, err := readJournal(journalPath)
	if err != nil || preparedJournal.Phase != journalPhasePrepared || !applyRequestsEqual(preparedJournal.Request, request) {
		return result, errors.New("prepared apply journal does not match its request")
	}
	if currentHash, hashErr := digestFile(filepath.Join(request.InstallRoot, StateName)); hashErr != nil || currentHash != request.PriorStateSHA256 {
		return result, errors.New("installed state changed before apply acknowledgement")
	}
	if active, activeInspection, activeErr := ActiveExecutableForRole(request.InstallRoot, request.CurrentRole); activeErr != nil || !activeInspection.TrustedIdentity || !samePath(active, request.CurrentExecutable) {
		return result, errors.New("installed app/runtime changed before apply acknowledgement")
	}
	if _, _, stageErr := LoadVerifiedStage(request.InstallRoot, request.StageRecord); stageErr != nil {
		return result, stageErr
	}
	previous, err = readTrustedState(request.InstallRoot, true)
	if err != nil {
		return result, err
	}
	watch, err := watchProcess(request.CurrentPID, request.CurrentExecutable, request.CurrentProcessToken)
	if err != nil {
		return result, err
	}
	defer watch.Close()
	if request.OwnedChildPID > 0 {
		childWatch, err = watchProcess(request.OwnedChildPID, request.OwnedChildExecutable, request.OwnedChildToken)
		if err != nil {
			return result, err
		}
		defer childWatch.Close()
	}
	for _, claim := range request.AdditionalProcesses {
		expected, _, expectedErr := ActiveExecutableForRole(request.InstallRoot, claim.Role)
		if expectedErr != nil || !samePath(expected, claim.Executable) {
			return result, errors.New("installed update participant changed before apply acknowledgement")
		}
		participant, watchErr := watchProcess(claim.PID, claim.Executable, claim.ProcessToken)
		if watchErr != nil {
			return result, watchErr
		}
		additionalWatches = append(additionalWatches, participantWatch{role: claim.Role, watch: participant})
		defer participant.Close()
	}
	defer recoverFailure()
	journal = ApplyJournal{Schema: SchemaVersion, Product: "Headroom", Phase: journalPhaseWaitingForExit, Request: request, Previous: previous}
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	if err = writeDurableJSON(request.AcknowledgementPath, map[string]any{"schema": SchemaVersion, "product": "Headroom", "accepted": true, "nonce": request.Nonce}); err != nil {
		return result, err
	}
	if err = waitForApplyCommit(request, time.Duration(request.CommitTimeoutMS)*time.Millisecond); err != nil {
		return result, err
	}
	if err = watch.Wait(time.Duration(request.WaitTimeoutMS) * time.Millisecond); err != nil {
		return result, err
	}
	appExited = true
	journal.Phase = journalPhaseCurrentExited
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	if childWatch != nil {
		if err = childWatch.Wait(time.Duration(request.WaitTimeoutMS) * time.Millisecond); err != nil {
			return result, err
		}
		childExited = true
	}
	journal.Phase = journalPhaseStoppingParticipants
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	for _, participant := range additionalWatches {
		if participant.role != RoleManagedServer {
			continue
		}
		if request.ManagedServiceSupervisor != "" {
			if err = stopManagedServiceSupervisor(request.ManagedServiceSupervisor); err == nil {
				err = participant.watch.Wait(time.Duration(request.WaitTimeoutMS) * time.Millisecond)
			}
		} else {
			err = participant.watch.KillWait(time.Duration(request.WaitTimeoutMS) * time.Millisecond)
		}
		if err != nil {
			return result, err
		}
	}
	for _, participant := range additionalWatches {
		if participant.role == RoleManagedServer {
			continue
		}
		err = participant.watch.Wait(time.Duration(request.WaitTimeoutMS) * time.Millisecond)
		if err != nil {
			return result, err
		}
	}
	journal.Phase = journalPhaseParticipantsStopped
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	stage, manifest, err := LoadVerifiedStage(request.InstallRoot, request.StageRecord)
	if err != nil {
		return result, err
	}
	journal.Phase = journalPhaseCopying
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	candidate, generationDir, err := createGeneration(request.InstallRoot, stage, manifest)
	if err != nil {
		return result, err
	}
	candidate.ApplicationEntryPath = previous.ApplicationEntryPath
	candidate.CLIEntryPath = previous.CLIEntryPath
	candidate.ServerEntryPath = previous.ServerEntryPath
	journal.Candidate, journal.GenerationDir = &candidate, generationDir
	journal.Phase = journalPhaseGenerationReady
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	if err = prepareBootstrapBackupWithCLI(request.InstallRoot, request.EntryPath, previous.CLIEntryPath, filepath.Dir(journalPath)); err != nil {
		_ = os.RemoveAll(generationDir)
		return result, err
	}
	journal.Phase = journalPhaseBootstrapIntent
	if err = writeApplyJournal(journalPath, journal); err != nil {
		_ = os.RemoveAll(generationDir)
		return result, err
	}
	if err = applyBootstrapWithCLI(stage.PackageRoot, request.InstallRoot, request.EntryPath, previous.CLIEntryPath, manifest); err != nil {
		_ = restoreBootstrapWithCLI(request.InstallRoot, request.EntryPath, previous.CLIEntryPath, filepath.Dir(journalPath))
		_ = os.RemoveAll(generationDir)
		return result, err
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("bootstrap-applied"); err != nil {
			return result, err
		}
	}
	journal.Phase = journalPhaseBootstrapReady
	if err = writeApplyJournal(journalPath, journal); err != nil {
		_ = restoreBootstrapWithCLI(request.InstallRoot, request.EntryPath, previous.CLIEntryPath, filepath.Dir(journalPath))
		return result, err
	}
	journal.Phase = journalPhaseStateIntent
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	if err = writeDurableJSON(filepath.Join(request.InstallRoot, StateName), candidate); err != nil {
		_ = restoreBootstrapWithCLI(request.InstallRoot, request.EntryPath, previous.CLIEntryPath, filepath.Dir(journalPath))
		return result, err
	}
	if transactionTestHook != nil {
		if err = transactionTestHook("state-applied"); err != nil {
			return result, err
		}
	}
	journal.Phase = journalPhaseActivated
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	if err = launchAndAwaitReady(request, candidate, func(pid int, executable, token string) error {
		journal.CandidatePID, journal.CandidateExe, journal.CandidateToken = pid, executable, token
		journal.Phase = journalPhaseCandidateLaunched
		return writeApplyJournal(journalPath, journal)
	}); err != nil {
		return result, err
	}
	if hasManagedService(request) {
		if err = launchManagedService(request, candidate, func(pid int, executable, token string) error {
			journal.ServicePID, journal.ServiceExe, journal.ServiceToken = pid, executable, token
			return writeApplyJournal(journalPath, journal)
		}); err != nil {
			return result, err
		}
	}
	journal.Phase = journalPhaseComplete
	if err = writeApplyJournal(journalPath, journal); err != nil {
		return result, err
	}
	_ = os.RemoveAll(filepath.Dir(filepath.Dir(stage.PackageRoot)))
	result = ApplyResult{Schema: SchemaVersion, Product: "Headroom", Status: "applied", Version: candidate.ActiveVersion, VersionPath: candidate.VersionPath}
	_ = writeDurableJSON(filepath.Join(request.InstallRoot, "last-apply-result.json"), result)
	complete = true
	return result, nil
}

func verifyBootstrapRestore(root, entry, directory string) error {
	return verifyBootstrapRestoreWithCLI(root, entry, "", directory)
}

func verifyBootstrapRestoreWithCLI(root, entry, cliEntry, directory string) error {
	targets := stableBootstrapTargetsWithCLI(root, entry, cliEntry)
	data, err := readBoundedFile(filepath.Join(directory, "bootstrap-backup.json"), maxManifestBytes)
	if err != nil {
		return err
	}
	var records []bootstrapBackupRecord
	if json.Unmarshal(data, &records) != nil || len(records) != len(targets) {
		return errors.New("bootstrap backup record is invalid")
	}
	for index, target := range targets {
		if !records[index].Existed {
			if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
				return errors.New("bootstrap absence was not restored")
			}
			continue
		}
		want, hashErr := digestFile(filepath.Join(directory, fmt.Sprintf("backup-%d", index)))
		got, targetErr := digestFile(target)
		backupInfo, backupErr := os.Lstat(filepath.Join(directory, fmt.Sprintf("backup-%d", index)))
		targetInfo, infoErr := os.Lstat(target)
		if hashErr != nil || targetErr != nil || backupErr != nil || infoErr != nil || want != got || !backupModesEqual(backupInfo.Mode(), targetInfo.Mode()) {
			return errors.New("bootstrap contents were not restored")
		}
	}
	return nil
}

func applyRequestsEqual(left, right ApplyRequest) bool {
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

func readStateUnchecked(root string) (InstallState, error) {
	data, err := readBoundedFile(filepath.Join(root, StateName), maxManifestBytes)
	if err != nil {
		return InstallState{}, err
	}
	var state InstallState
	if json.Unmarshal(data, &state) != nil {
		return InstallState{}, errors.New("installed state is invalid")
	}
	return state, nil
}

func statesEqual(left, right InstallState) bool {
	return left == right
}

func recoverInstallJournal(journal InstallJournal, directory string) error {
	if journal.Phase == journalPhaseBootstrapIntent || journal.Phase == journalPhaseStateIntent {
		statePath := filepath.Join(journal.InstallRoot, StateName)
		currentPrior := false
		if journal.PriorStateExisted {
			digest, err := digestFile(statePath)
			currentPrior = err == nil && digest == journal.PriorStateSHA256
		} else {
			_, err := os.Lstat(statePath)
			currentPrior = errors.Is(err, os.ErrNotExist)
		}
		current, currentErr := readStateUnchecked(journal.InstallRoot)
		currentCandidate := currentErr == nil && statesEqual(current, journal.Candidate)
		if !currentPrior && !currentCandidate {
			journal.Phase = journalPhaseObsolete
			return writeDurableJSON(filepath.Join(directory, InstallJournalName), journal)
		}
		if err := restoreBootstrapWithCLI(journal.InstallRoot, journal.EntryPath, journal.CLIEntryPath, directory); err != nil {
			return err
		}
		if journal.PriorStateExisted {
			if err := replaceFile(filepath.Join(directory, "prior-install-state.json"), statePath, 0o600); err != nil {
				return err
			}
		} else if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if journal.GenerationDir != "" {
		if err := os.RemoveAll(journal.GenerationDir); err != nil {
			return err
		}
	}
	journal.Phase = journalPhaseRolledBack
	return writeDurableJSON(filepath.Join(directory, InstallJournalName), journal)
}

func createGeneration(root string, stage StageResult, manifest PackageManifest) (InstallState, string, error) {
	versions, err := ensureOwnedDirectory(root, "versions", 0o755)
	if err != nil {
		return InstallState{}, "", err
	}
	token, err := randomHex(8)
	if err != nil {
		return InstallState{}, "", err
	}
	name := manifest.Version + ".generation-" + stage.ManifestSHA256[:16] + "-" + token
	versionPath := filepath.ToSlash(filepath.Join("versions", name))
	destination := filepath.Join(versions, name)
	if err = copyTree(filepath.Join(stage.PackageRoot, "bundle"), destination); err != nil {
		_ = os.RemoveAll(destination)
		return InstallState{}, "", err
	}
	if err = copyFile(filepath.Join(stage.PackageRoot, PackageManifestName), filepath.Join(destination, PackageManifestName), 0o644); err != nil {
		_ = os.RemoveAll(destination)
		return InstallState{}, "", err
	}
	state := InstallState{PackageKind: manifest.PackageKind, Schema: SchemaVersion, Product: "Headroom", Platform: manifest.Platform, Architecture: manifest.Architecture,
		ActiveVersion: manifest.Version, VersionPath: versionPath, ManifestSHA256: stage.ManifestSHA256, PackageAsset: manifest.AssetName}
	if _, err = inspectState(root, state); err != nil {
		_ = os.RemoveAll(destination)
		return InstallState{}, "", err
	}
	return state, destination, nil
}

func readTrustedState(root string, requireLaunchable bool) (*InstallState, error) {
	data, err := readBoundedFile(filepath.Join(root, StateName), maxManifestBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !requireLaunchable {
			return nil, nil
		}
		return nil, err
	}
	var state InstallState
	if json.Unmarshal(data, &state) != nil {
		return nil, errors.New("installed state is invalid")
	}
	inspection, err := inspectState(root, state)
	if err != nil || !inspection.TrustedIdentity {
		return nil, errors.New("installed state is not trusted")
	}
	if requireLaunchable {
		for _, missing := range inspection.Missing {
			if !isAuxiliaryPath(missing) {
				return nil, errors.New("installed app/runtime is not a rollback baseline")
			}
		}
	}
	return &state, nil
}

func readApplyRequest(path string) (ApplyRequest, string, error) {
	path, err := normalizeAbsolutePath(path, "apply request")
	if err != nil {
		return ApplyRequest{}, "", err
	}
	data, err := readBoundedFile(path, maxManifestBytes)
	if err != nil {
		return ApplyRequest{}, "", errors.New("apply request is unavailable or oversized")
	}
	var request ApplyRequest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&request) != nil || ensureEOF(decoder) != nil || request.Schema != SchemaVersion || request.Product != "Headroom" {
		return ApplyRequest{}, "", errors.New("apply request is invalid")
	}
	root, err := NormalizeInstallRoot(request.InstallRoot)
	if err != nil {
		return ApplyRequest{}, "", err
	}
	transactionRoot := filepath.Join(root, "transactions")
	directoryName := filepath.Base(filepath.Dir(path))
	if filepath.Dir(filepath.Dir(path)) != transactionRoot || !strings.HasPrefix(directoryName, "apply-") || len(directoryName) != 38 || filepath.Base(path) != TransactionRequestName {
		return ApplyRequest{}, "", errors.New("apply request is outside the transaction root")
	}
	request.InstallRoot = root
	directory := filepath.Dir(path)
	if err = validateApplyRequestFields(&request, root, directory); err != nil {
		return ApplyRequest{}, "", err
	}
	return request, filepath.Join(filepath.Dir(path), TransactionJournalName), nil
}

func readJournal(path string) (ApplyJournal, error) {
	data, err := readBoundedFile(path, maxManifestBytes)
	if err != nil {
		return ApplyJournal{}, errors.New("apply journal is unavailable or oversized")
	}
	var journal ApplyJournal
	if json.Unmarshal(data, &journal) != nil || journal.Schema != SchemaVersion || journal.Product != "Headroom" {
		return ApplyJournal{}, errors.New("apply journal is invalid")
	}
	return journal, nil
}

func readInstallJournal(path, root, directory string) (InstallJournal, error) {
	data, err := readBoundedFile(path, maxManifestBytes)
	if err != nil {
		return InstallJournal{}, errors.New("install journal is unavailable or oversized")
	}
	var journal InstallJournal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&journal) != nil || ensureEOF(decoder) != nil || journal.Schema != SchemaVersion || journal.Product != "Headroom" {
		return InstallJournal{}, errors.New("install journal is invalid")
	}
	if journal.InstallRoot != root || filepath.Dir(directory) != filepath.Join(root, "transactions") ||
		!strings.HasPrefix(filepath.Base(directory), "install-") || len(filepath.Base(directory)) != 24 {
		return InstallJournal{}, errors.New("install journal root is invalid")
	}
	if journal.EntryPath != "" {
		if normalized, pathErr := normalizeAbsolutePath(journal.EntryPath, "entry path"); pathErr != nil || normalized != journal.EntryPath {
			return InstallJournal{}, errors.New("install journal entry is invalid")
		}
	}
	if journal.CLIEntryPath != "" {
		if normalized, pathErr := normalizeAbsolutePath(journal.CLIEntryPath, "CLI entry path"); pathErr != nil || normalized != journal.CLIEntryPath {
			return InstallJournal{}, errors.New("install journal CLI entry is invalid")
		}
	}
	if journal.GenerationDir != filepath.Join(root, filepath.FromSlash(journal.Candidate.VersionPath)) || !validVersionPath(journal.Candidate) {
		return InstallJournal{}, errors.New("install journal generation is invalid")
	}
	if inspection, inspectErr := inspectState(root, journal.Candidate); inspectErr != nil || !inspection.TrustedIdentity {
		return InstallJournal{}, errors.New("install journal candidate is invalid")
	}
	validPhases := map[journalPhase]bool{journalPhaseGenerationReady: true, journalPhaseBootstrapIntent: true, journalPhaseStateIntent: true, journalPhaseComplete: true, journalPhaseRolledBack: true, journalPhaseObsolete: true}
	if !validPhases[journal.Phase] {
		return InstallJournal{}, errors.New("install journal phase is invalid")
	}
	if journal.PriorStateExisted {
		if !hashPattern.MatchString(journal.PriorStateSHA256) {
			return InstallJournal{}, errors.New("install journal prior state hash is invalid")
		}
		if info, stateErr := os.Lstat(filepath.Join(directory, "prior-install-state.json")); stateErr != nil || !info.Mode().IsRegular() {
			return InstallJournal{}, errors.New("install journal prior state is invalid")
		}
	} else if journal.PriorStateSHA256 != "" {
		return InstallJournal{}, errors.New("install journal prior state identity is invalid")
	}
	for _, name := range journal.Supersedes {
		if !(strings.HasPrefix(name, "apply-") && len(name) == 38 || strings.HasPrefix(name, "install-") && len(name) == 24) || filepath.Base(name) != name {
			return InstallJournal{}, errors.New("install journal superseded transaction is invalid")
		}
	}
	return journal, nil
}

func validateApplyJournal(journal ApplyJournal, root, directory string) error {
	if journal.Request.InstallRoot != root || filepath.Dir(directory) != filepath.Join(root, "transactions") || journal.Previous == nil {
		return errors.New("apply journal root is invalid")
	}
	if !validVersionPath(*journal.Previous) {
		return errors.New("apply rollback state is invalid")
	}
	validPhases := map[journalPhase]bool{journalPhaseWaitingForExit: true, journalPhaseCurrentExited: true, journalPhaseStoppingParticipants: true, journalPhaseParticipantsStopped: true, journalPhaseCopying: true, journalPhaseGenerationReady: true, journalPhaseBootstrapIntent: true,
		journalPhaseBootstrapReady: true, journalPhaseStateIntent: true, journalPhaseActivated: true, journalPhaseCandidateLaunched: true, journalPhaseRollingBack: true,
		journalPhaseRollbackRelaunch: true, journalPhaseComplete: true, journalPhaseRolledBack: true, journalPhaseObsolete: true}
	if !validPhases[journal.Phase] {
		return errors.New("apply journal phase is invalid")
	}
	if err := validateApplyRequestFields(&journal.Request, root, directory); err != nil {
		return err
	}
	previousInspection, previousErr := inspectState(root, *journal.Previous)
	if previousErr != nil || !previousInspection.TrustedIdentity {
		return errors.New("apply rollback identity is invalid")
	}
	previousManifest, manifestErr := installedManifest(root, *journal.Previous)
	if manifestErr != nil {
		return errors.New("apply prior manifest is invalid")
	}
	previousPath := previousManifest.Components.Application.Path
	if journal.Request.CurrentRole == RoleCLI {
		previousPath = PackageCLIPath(previousManifest.Platform, previousManifest.PackageKind)
	}
	if journal.Request.CurrentRole == RoleServer {
		previousPath = previousManifest.Components.Server.Path
	}
	if !samePath(journal.Request.CurrentExecutable, installedComponentPath(root, journal.Previous.VersionPath, previousPath)) {
		return errors.New("apply prior executable is invalid")
	}
	if journal.Request.OwnedChildPID > 0 {
		if !samePath(journal.Request.OwnedChildExecutable, installedComponentPath(root, journal.Previous.VersionPath, previousManifest.Components.Server.Path)) {
			return errors.New("apply owned child executable is invalid")
		}
	}
	for _, claim := range journal.Request.AdditionalProcesses {
		expected, expectedErr := executableForStateRole(root, *journal.Previous, previousManifest, claim.Role)
		if expectedErr != nil || !samePath(claim.Executable, expected) {
			return errors.New("apply participant executable is invalid")
		}
	}
	candidateRemoved := false
	if journal.Candidate != nil {
		expected := filepath.Join(root, filepath.FromSlash(journal.Candidate.VersionPath))
		if !validVersionPath(*journal.Candidate) || journal.GenerationDir != expected {
			return errors.New("apply candidate path is invalid")
		}
		if journal.Phase == journalPhaseRollingBack || journal.Phase == journalPhaseRollbackRelaunch || journal.Phase == journalPhaseRolledBack {
			current, stateErr := readStateUnchecked(root)
			_, generationErr := os.Lstat(expected)
			candidateRemoved = stateErr == nil && statesEqual(current, *journal.Previous) && errors.Is(generationErr, os.ErrNotExist) &&
				(journal.CandidatePID <= 0 || processGone(journal.CandidatePID)) && (journal.ServicePID <= 0 || processGone(journal.ServicePID))
		}
		if candidateInspection, candidateErr := inspectState(root, *journal.Candidate); !candidateRemoved && (candidateErr != nil || !candidateInspection.TrustedIdentity) {
			return errors.New("apply candidate identity is invalid")
		}
	}
	if journal.CandidatePID > 0 && (journal.CandidateExe == "" || journal.CandidateToken == "") {
		return errors.New("apply candidate process identity is invalid")
	}
	if journal.ServicePID > 0 && (journal.ServiceExe == "" || journal.ServiceToken == "" || journal.Candidate == nil) {
		return errors.New("managed service candidate identity is invalid")
	}
	if journal.ServicePID > 0 && journal.Candidate != nil && !candidateRemoved {
		candidateManifest, candidateErr := installedManifest(root, *journal.Candidate)
		expected := installedComponentPath(root, journal.Candidate.VersionPath, PackageCLIPath(candidateManifest.Platform, candidateManifest.PackageKind))
		if candidateErr != nil || !samePath(journal.ServiceExe, expected) {
			return errors.New("managed service candidate executable is invalid")
		}
	}
	if journal.CandidatePID > 0 && journal.Candidate != nil && !candidateRemoved {
		candidateManifest, candidateErr := installedManifest(root, *journal.Candidate)
		candidatePath := candidateManifest.Components.Application.Path
		if journal.Request.CandidateRole == RoleCLI {
			candidatePath = PackageCLIPath(candidateManifest.Platform, candidateManifest.PackageKind)
		}
		if journal.Request.CandidateRole == RoleServer {
			candidatePath = candidateManifest.Components.Server.Path
		}
		if candidateErr != nil || !samePath(journal.CandidateExe, installedComponentPath(root, journal.Candidate.VersionPath, candidatePath)) {
			return errors.New("apply candidate executable is invalid")
		}
	}
	return nil
}

func validateApplyRequestFields(request *ApplyRequest, root, directory string) error {
	var err error
	request.CurrentRole = normalizedProcessRole(request.CurrentRole)
	request.CandidateRole = normalizedProcessRole(request.CandidateRole)
	if !validProcessRole(request.CurrentRole) || !validProcessRole(request.CandidateRole) {
		return errors.New("apply process role is invalid")
	}
	request.EntryPath, err = normalizeAbsolutePath(request.EntryPath, "entry path")
	if err != nil {
		return err
	}
	if err = validateInstallTargets(root, request.EntryPath); err != nil {
		return err
	}
	if err = validateInstallTargets(directory, ""); err != nil {
		return err
	}
	request.CurrentExecutable, err = normalizeAbsolutePath(request.CurrentExecutable, "current executable")
	if err != nil || request.CurrentPID <= 0 || request.CurrentProcessToken == "" || len(request.RelaunchArguments) > 32 {
		return errors.New("apply process identity is invalid")
	}
	if request.OwnedChildPID > 0 {
		request.OwnedChildExecutable, err = normalizeAbsolutePath(request.OwnedChildExecutable, "owned child executable")
		if err != nil || request.OwnedChildToken == "" {
			return errors.New("owned child identity is invalid")
		}
	} else if request.OwnedChildExecutable != "" || request.OwnedChildToken != "" {
		return errors.New("owned child identity is incomplete")
	}
	if len(request.AdditionalProcesses) > 4 {
		return errors.New("too many additional update participants")
	}
	seenPID := map[int]bool{request.CurrentPID: true}
	seenRole := map[string]bool{request.CurrentRole: true}
	if request.OwnedChildPID > 0 {
		seenPID[request.OwnedChildPID] = true
		seenRole[RoleServer] = true
	}
	for index := range request.AdditionalProcesses {
		claim := &request.AdditionalProcesses[index]
		claim.Role = normalizedProcessRole(claim.Role)
		claim.Executable, err = normalizeAbsolutePath(claim.Executable, "participant executable")
		if err != nil || !validParticipantRole(claim.Role) || claim.PID <= 0 || claim.ProcessToken == "" || seenPID[claim.PID] || seenRole[claim.Role] {
			return errors.New("additional update participant identity is invalid")
		}
		seenPID[claim.PID], seenRole[claim.Role] = true, true
	}
	if err := validateServiceArguments(request.ManagedServiceArguments); err != nil {
		return err
	}
	if err := validateServiceEnvironment(request.ManagedServiceEnvironment); err != nil {
		return err
	}
	if request.ManagedServiceSupervisor != "" && request.ManagedServiceSupervisor != managedSystemdUser {
		return errors.New("managed service supervisor is invalid")
	}
	if len(request.ManagedServiceArguments) != 0 && !seenRole[RoleManagedServer] {
		return errors.New("managed service arguments have no participant")
	}
	if seenRole[RoleManagedServer] {
		working, workingErr := normalizeAbsolutePath(request.ManagedServiceWorkingDirectory, "managed service working directory")
		if workingErr != nil || working != request.ManagedServiceWorkingDirectory {
			return errors.New("managed service working directory is invalid")
		}
	} else if len(request.ManagedServiceEnvironment) != 0 || request.ManagedServiceWorkingDirectory != "" || request.ManagedServiceSupervisor != "" {
		return errors.New("managed service restart configuration has no participant")
	}
	for _, argument := range request.RelaunchArguments {
		if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return errors.New("apply relaunch argument is invalid")
		}
	}
	if request.InstallRoot != root || request.WaitTimeoutMS <= 0 || request.WaitTimeoutMS > 120000 || request.CommitTimeoutMS <= 0 || request.CommitTimeoutMS > 30000 || request.ReadyTimeoutMS <= 0 || request.ReadyTimeoutMS > 120000 ||
		len(request.Nonce) != 48 || !isLowerHex(request.Nonce) || !hashPattern.MatchString(request.PriorStateSHA256) ||
		!samePath(request.AcknowledgementPath, filepath.Join(directory, "accepted.json")) || !samePath(request.CommitPath, filepath.Join(directory, "commit.json")) ||
		!samePath(request.ReadyPath, filepath.Join(directory, "ready.json")) {
		return errors.New("apply request bounds or private paths are invalid")
	}
	return nil
}

func normalizedProcessRole(role string) string {
	if role == "" {
		return RoleApplication
	}
	return role
}

func validProcessRole(role string) bool {
	return role == RoleApplication || role == RoleCLI || role == RoleServer
}

func validParticipantRole(role string) bool {
	return validProcessRole(role) || role == RolePublicLauncher || role == RoleManagedServer || role == RoleManagedLauncher
}

func executableForStateRole(root string, state InstallState, manifest PackageManifest, role string) (string, error) {
	if role == RolePublicLauncher || role == RoleManagedLauncher {
		if state.CLIEntryPath == "" {
			return "", errors.New("trusted public CLI entry is not recorded")
		}
		return state.CLIEntryPath, nil
	}
	path := manifest.Components.Application.Path
	if role == RoleCLI || role == RoleManagedServer {
		path = PackageCLIPath(manifest.Platform, manifest.PackageKind)
	}
	if role == RoleServer {
		path = manifest.Components.Server.Path
	}
	return installedComponentPath(root, state.VersionPath, path), nil
}

func isLowerHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func randomHex(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func boundedError(err error) string {
	value := err.Error()
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func samePath(left, right string) bool {
	if runtimeWindows() {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func isDirectOwnedRecord(staging, record string) bool {
	directory := filepath.Dir(record)
	return filepath.Dir(directory) == staging && strings.HasPrefix(filepath.Base(directory), "package-") && filepath.Base(record) == "verified-stage.json"
}

func ensureOwnedDirectory(root, child string, mode os.FileMode) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := validateInstallTargets(root, ""); err != nil {
		return "", err
	}
	path := filepath.Join(root, child)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(path, mode); err != nil {
			return "", err
		}
		return path, nil
	}
	if err != nil || !info.IsDir() || pathIsLinkOrReparse(path, info) {
		return "", fmt.Errorf("owned install directory is unsafe: %s", path)
	}
	return path, nil
}

type bootstrapBackupRecord struct {
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode"`
}

func stableBootstrapTargets(root, entry string) []string {
	ext := ""
	launcherName := "headroom-launcher"
	if runtimeWindows() {
		ext = ".exe"
		launcherName = "headroom"
	}
	launcher := filepath.Join(root, launcherName+ext)
	values := []string{launcher, filepath.Join(root, "headroom-package"+ext), launcher + ".root"}
	if entry != "" && !samePath(entry, launcher) {
		values = append(values, entry, entry+".root")
	}
	return values
}

func stableBootstrapTargetsWithCLI(root, entry, cliEntry string) []string {
	values := stableBootstrapTargets(root, entry)
	if cliEntry == "" {
		return values
	}
	// A CLI-only install shares one path between the application entry and the
	// CLI entry. The server compatibility entry is still replaced on apply, so
	// it must stay in this list or a rollback would leave it on the new version.
	add := func(candidate string) {
		for _, existing := range values {
			if samePath(existing, candidate) {
				return
			}
		}
		values = append(values, candidate)
	}
	add(cliEntry)
	add(cliEntry + ".root")
	if !runtimeWindows() {
		serverEntry := filepath.Join(filepath.Dir(cliEntry), "usage-server")
		add(serverEntry)
		add(serverEntry + ".root")
	}
	return values
}

func prepareBootstrapBackup(root, entry, backupDir string) error {
	return prepareBootstrapBackupWithCLI(root, entry, "", backupDir)
}

func prepareBootstrapBackupWithCLI(root, entry, cliEntry, backupDir string) error {
	targets := stableBootstrapTargetsWithCLI(root, entry, cliEntry)
	records := make([]bootstrapBackupRecord, len(targets))
	for index, target := range targets {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("stable entry is unsafe: %s", target)
		}
		records[index] = bootstrapBackupRecord{Existed: true, Mode: uint32(info.Mode().Perm())}
		if err = copyFile(target, filepath.Join(backupDir, fmt.Sprintf("backup-%d", index)), info.Mode().Perm()); err != nil {
			return err
		}
	}
	if err := writeDurableJSON(filepath.Join(backupDir, "bootstrap-backup.json"), records); err != nil {
		return err
	}
	return nil
}

func applyBootstrap(packageRoot, root, entry string, manifest PackageManifest) error {
	return applyBootstrapWithCLI(packageRoot, root, entry, "", manifest)
}

func applyBootstrapWithCLI(packageRoot, root, entry, cliEntry string, manifest PackageManifest) error {
	targets := stableBootstrapTargets(root, entry)
	ext := ""
	launcherName := "headroom"
	if runtimeWindows() {
		ext = ".exe"
	}
	launcherSource := filepath.Join(packageRoot, "bootstrap", launcherName+ext)
	managerSource := filepath.Join(packageRoot, "bootstrap", "headroom-package"+ext)
	sources := []string{launcherSource, managerSource, "association"}
	if len(targets) > 3 {
		sources = append(sources, launcherSource, "association")
	}
	for index, target := range targets {
		if sources[index] == "association" {
			if err := replaceBytes([]byte(root+"\n"), target, 0o600); err != nil {
				return err
			}
		} else if err := replaceFile(sources[index], target, 0o755); err != nil {
			return err
		}
	}
	if err := verifyInstalledBootstrap(root, entry, manifest); err != nil {
		return err
	}
	if cliEntry != "" && !samePath(cliEntry, entry) {
		recordPath := "bootstrap/headroom-cli" + ext
		if manifest.PackageKind == PackageKindCLI {
			recordPath = "bootstrap/headroom" + ext
		}
		record, ok := fileRecord(manifest.Files, recordPath)
		if !ok || record.LinkTarget != "" {
			return errors.New("verified package CLI bootstrap inventory is incomplete")
		}
		source := filepath.Join(packageRoot, filepath.FromSlash(recordPath))
		if err := replaceFile(source, cliEntry, 0o755); err != nil {
			return err
		}
		if err := replaceBytes([]byte(root+"\n"), cliEntry+".root", 0o600); err != nil {
			return err
		}
		if err := verifyStableEntry(root, cliEntry, manifest, recordPath); err != nil {
			return err
		}
	}
	if cliEntry != "" && !runtimeWindows() {
		serverEntry := filepath.Join(filepath.Dir(cliEntry), "usage-server")
		recordPath := "bootstrap/headroom-cli"
		if manifest.PackageKind == PackageKindCLI {
			recordPath = "bootstrap/headroom"
		}
		if err := replaceFile(filepath.Join(packageRoot, filepath.FromSlash(recordPath)), serverEntry, 0o755); err != nil {
			return err
		}
		if err := replaceBytes([]byte(root+"\n"), serverEntry+".root", 0o600); err != nil {
			return err
		}
		if err := verifyStableEntry(root, serverEntry, manifest, recordPath); err != nil {
			return err
		}
	}
	return nil
}

func installBootstrap(packageRoot, root, entry, backupDir string) error {
	if err := prepareBootstrapBackup(root, entry, backupDir); err != nil {
		return err
	}
	manifestFile, err := os.Open(filepath.Join(packageRoot, PackageManifestName))
	if err != nil {
		return err
	}
	manifest, err := DecodePackageManifest(manifestFile)
	manifestFile.Close()
	if err != nil {
		return err
	}
	return applyBootstrap(packageRoot, root, entry, manifest)
}

func verifyInstalledBootstrap(root, entry string, manifest PackageManifest) error {
	ext := ""
	if manifest.Platform == "windows" {
		ext = ".exe"
	}
	find := func(path string) (File, bool) {
		for _, record := range manifest.Files {
			if record.Path == path {
				return record, true
			}
		}
		return File{}, false
	}
	launcherRecord, launcherOK := find("bootstrap/headroom" + ext)
	managerRecord, managerOK := find("bootstrap/headroom-package" + ext)
	if !launcherOK || !managerOK {
		return errors.New("verified package bootstrap inventory is incomplete")
	}
	launcher := filepath.Join(root, "headroom-launcher")
	if manifest.Platform == "windows" {
		launcher = filepath.Join(root, "headroom.exe")
	}
	checks := []struct {
		path   string
		record File
	}{{launcher, launcherRecord}, {filepath.Join(root, "headroom-package"+ext), managerRecord}}
	if entry != "" && !samePath(entry, launcher) {
		checks = append(checks, struct {
			path   string
			record File
		}{entry, launcherRecord})
	}
	for _, check := range checks {
		info, err := os.Lstat(check.path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != check.record.Size {
			return fmt.Errorf("installed bootstrap is incomplete: %s", check.path)
		}
		if manifest.Platform != "windows" && fmt.Sprintf("%04o", info.Mode().Perm()) != check.record.Mode {
			return fmt.Errorf("installed bootstrap mode mismatch: %s", check.path)
		}
		digest, err := digestFile(check.path)
		if err != nil || digest != check.record.SHA256 {
			return fmt.Errorf("installed bootstrap hash mismatch: %s", check.path)
		}
	}
	associations := []string{launcher + ".root"}
	if entry != "" && !samePath(entry, launcher) {
		associations = append(associations, entry+".root")
	}
	for _, association := range associations {
		data, err := readBoundedFile(association, 4096)
		if err != nil || string(data) != root+"\n" {
			return fmt.Errorf("installed bootstrap association is invalid: %s", association)
		}
	}
	return nil
}

func restoreBootstrap(root, entry, backupDir string) error {
	return restoreBootstrapWithCLI(root, entry, "", backupDir)
}

func restoreBootstrapWithCLI(root, entry, cliEntry, backupDir string) error {
	targets := stableBootstrapTargetsWithCLI(root, entry, cliEntry)
	data, err := readBoundedFile(filepath.Join(backupDir, "bootstrap-backup.json"), maxManifestBytes)
	if err != nil {
		return err
	}
	var records []bootstrapBackupRecord
	if json.Unmarshal(data, &records) != nil || len(records) != len(targets) {
		return errors.New("bootstrap backup record is invalid")
	}
	for index, target := range targets {
		if records[index].Existed {
			mode := os.FileMode(records[index].Mode)
			if !validBackupMode(mode) {
				return errors.New("bootstrap backup mode is invalid")
			}
			if err = replaceFile(filepath.Join(backupDir, fmt.Sprintf("backup-%d", index)), target, mode); err != nil {
				return err
			}
		} else if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func rollbackJournal(journal ApplyJournal, directory string) error {
	if journal.Previous == nil || journal.Candidate == nil {
		return errors.New("apply journal has no rollback state")
	}
	cliEntry := ""
	if journal.Previous != nil {
		cliEntry = journal.Previous.CLIEntryPath
	}
	if err := restoreBootstrapWithCLI(journal.Request.InstallRoot, journal.Request.EntryPath, cliEntry, directory); err != nil {
		return err
	}
	if err := writeDurableJSON(filepath.Join(journal.Request.InstallRoot, StateName), journal.Previous); err != nil {
		return err
	}
	if journal.GenerationDir != "" {
		expected := filepath.Join(journal.Request.InstallRoot, filepath.FromSlash(journal.Candidate.VersionPath))
		if !samePath(journal.GenerationDir, expected) {
			return errors.New("journal generation path is invalid")
		}
		if transactionTestHook != nil {
			if err := transactionTestHook("rollback-remove-generation"); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(expected); err != nil {
			return err
		}
		if transactionTestHook != nil {
			if err := transactionTestHook("rollback-generation-removed"); err != nil {
				return err
			}
		}
	}
	journal.Phase = journalPhaseRollbackRelaunch
	return writeApplyJournal(filepath.Join(directory, TransactionJournalName), journal)
}

func launchAndAwaitReady(request ApplyRequest, state InstallState, onLaunch func(int, string, string) error) error {
	inspection, err := inspectState(request.InstallRoot, state)
	if err != nil {
		return err
	}
	manifest, err := installedManifest(request.InstallRoot, state)
	if err != nil {
		return err
	}
	packagePath := manifest.Components.Application.Path
	if request.CandidateRole == RoleCLI {
		packagePath = PackageCLIPath(manifest.Platform, manifest.PackageKind)
	}
	if request.CandidateRole == RoleServer {
		packagePath = manifest.Components.Server.Path
	}
	executable := installedComponentPath(request.InstallRoot, state.VersionPath, packagePath)
	for _, missing := range inspection.Missing {
		if !isAuxiliaryPath(missing) || missing == strings.TrimPrefix(packagePath, "bundle/") {
			return errors.New("target app/runtime is incomplete")
		}
	}
	_ = os.Remove(request.ReadyPath)
	environment := authoritativeApplyEnvironment(os.Environ(), map[string]string{
		"HEADROOM_INSTALL_ROOT": request.InstallRoot, "HEADROOM_LAUNCHER_PATH": request.EntryPath,
		"HEADROOM_PACKAGE_VERSION": state.ActiveVersion, "HEADROOM_READY_NONCE": request.Nonce,
	})
	arguments := append([]string{executable}, request.RelaunchArguments...)
	arguments = append(arguments, "--headroom-update-restart", "--headroom-ready-file", request.ReadyPath)
	process, err := os.StartProcess(executable, arguments, &os.ProcAttr{Env: environment, Files: []*os.File{nil, nil, nil}})
	if err != nil {
		return err
	}
	token, tokenErr := captureProcessToken(process.Pid, executable)
	if tokenErr != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return tokenErr
	}
	done := make(chan error, 1)
	go func() { _, waitErr := process.Wait(); done <- waitErr }()
	if onLaunch != nil {
		if err = onLaunch(process.Pid, executable, token); err != nil {
			_ = process.Kill()
			<-done
			return err
		}
	}
	deadline := time.Now().Add(time.Duration(request.ReadyTimeoutMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			return fmt.Errorf("replacement app exited before readiness: %v", waitErr)
		default:
		}
		data, readErr := readBoundedFile(request.ReadyPath, 4096)
		if readErr == nil {
			var ready struct {
				Nonce      string `json:"nonce"`
				PID        int    `json:"pid"`
				Version    string `json:"version"`
				Executable string `json:"executable"`
			}
			if json.Unmarshal(data, &ready) == nil && ready.Nonce == request.Nonce && ready.PID == process.Pid && ready.Version == state.ActiveVersion && samePath(ready.Executable, executable) {
				select {
				case waitErr := <-done:
					return fmt.Errorf("replacement app exited during readiness: %v", waitErr)
				case <-time.After(200 * time.Millisecond):
					return nil
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = process.Kill()
	<-done
	return errors.New("replacement app readiness timed out")
}

func hasManagedService(request ApplyRequest) bool {
	for _, claim := range request.AdditionalProcesses {
		if claim.Role == RoleManagedServer {
			return true
		}
	}
	return false
}

func finishManagedServiceStopForRecovery(root string, request ApplyRequest) error {
	if request.ManagedServiceSupervisor != "" {
		if err := stopManagedServiceSupervisor(request.ManagedServiceSupervisor); err != nil {
			return err
		}
	} else {
		for _, claim := range request.AdditionalProcesses {
			if claim.Role == RoleManagedServer {
				if err := stopRecordedProcess(claim.PID, claim.Executable, claim.ProcessToken, 5*time.Second); err != nil {
					return err
				}
				break
			}
		}
	}
	return clearManagedServiceReceipt(root)
}

func launchManagedService(request ApplyRequest, state InstallState, onLaunch func(int, string, string) error) error {
	manifest, err := installedManifest(request.InstallRoot, state)
	if err != nil {
		return err
	}
	executable := installedComponentPath(request.InstallRoot, state.VersionPath, PackageCLIPath(manifest.Platform, manifest.PackageKind))
	launchToken, err := randomHex(24)
	if err != nil {
		return err
	}
	if request.ManagedServiceSupervisor != "" {
		_ = clearSupervisedManagedServiceRestart(request.InstallRoot)
		if err = clearManagedServiceReceipt(request.InstallRoot); err != nil {
			return err
		}
		if err = authorizeSupervisedManagedServiceRestart(request.InstallRoot, executable, request.ManagedServiceArguments,
			request.ManagedServiceEnvironment, request.ManagedServiceWorkingDirectory); err != nil {
			return err
		}
		failed := func(cause error) error {
			return errors.Join(cause, stopManagedServiceSupervisor(request.ManagedServiceSupervisor), clearSupervisedManagedServiceRestart(request.InstallRoot))
		}
		if err := startManagedServiceSupervisor(request.ManagedServiceSupervisor); err != nil {
			return failed(err)
		}
		deadline := time.Now().Add(time.Duration(request.ReadyTimeoutMS) * time.Millisecond)
		journaled := false
		for time.Now().Before(deadline) {
			record, readErr := readManagedService(request.InstallRoot)
			if readErr == nil && record.Supervisor == request.ManagedServiceSupervisor && samePath(record.Executable, executable) &&
				slices.Equal(record.Arguments, request.ManagedServiceArguments) && slices.Equal(record.Environment, request.ManagedServiceEnvironment) &&
				record.WorkingDirectory == request.ManagedServiceWorkingDirectory {
				if !journaled && onLaunch != nil {
					if err = onLaunch(record.PID, record.Executable, record.ProcessToken); err != nil {
						return failed(err)
					}
					journaled = true
				}
				if record.Ready {
					return nil
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		return failed(errors.New("managed per-user headroom.service did not report the active generation"))
	}
	baseEnvironment := authoritativeServiceEnvironment(os.Environ(), request.ManagedServiceEnvironment)
	environment := authoritativeApplyEnvironment(baseEnvironment, map[string]string{
		"HEADROOM_INSTALL_ROOT": request.InstallRoot, "HEADROOM_LAUNCHER_PATH": state.CLIEntryPath,
		"HEADROOM_PACKAGE_VERSION": state.ActiveVersion, "HEADROOM_MANAGED_SERVICE_RESTART": launchToken,
	})
	arguments := append([]string{executable, "serve"}, request.ManagedServiceArguments...)
	process, err := os.StartProcess(executable, arguments, &os.ProcAttr{Dir: request.ManagedServiceWorkingDirectory, Env: environment, Files: []*os.File{nil, nil, nil}})
	if err != nil {
		return err
	}
	token, err := captureProcessToken(process.Pid, executable)
	if err != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return err
	}
	if onLaunch != nil {
		if err = onLaunch(process.Pid, executable, token); err != nil {
			_ = process.Kill()
			_, _ = process.Wait()
			return err
		}
	}
	owner, ownerErr := currentOwnerIdentity()
	if ownerErr != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return ownerErr
	}
	record := ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: request.InstallRoot, PID: process.Pid, Executable: executable, ProcessToken: token,
		Arguments: append([]string(nil), request.ManagedServiceArguments...), LaunchToken: launchToken, Owner: owner,
		Environment: append([]string(nil), request.ManagedServiceEnvironment...), WorkingDirectory: request.ManagedServiceWorkingDirectory}
	if err = writeManagedService(request.InstallRoot, record); err != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { _, waitErr := process.Wait(); done <- waitErr }()
	deadline := time.Now().Add(time.Duration(request.ReadyTimeoutMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-done:
			return fmt.Errorf("managed service exited during startup: %v", waitErr)
		default:
		}
		ready, readyErr := ReadManagedService(request.InstallRoot)
		if readyErr == nil && ready.Ready && ready.LaunchToken == "" && ready.PID == process.Pid && ready.ProcessToken == token && samePath(ready.Executable, executable) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = process.Kill()
	<-done
	return errors.New("managed service readiness timed out")
}

func authoritativeServiceEnvironment(base, saved []string) []string {
	result := make([]string, 0, len(base)+len(saved))
	for _, item := range base {
		if len(managedServiceEnvironment([]string{item})) == 0 {
			result = append(result, item)
		}
	}
	return append(result, saved...)
}

func authoritativeApplyEnvironment(base []string, values map[string]string) []string {
	managed := map[string]bool{"HEADROOM_INSTALL_ROOT": true, "HEADROOM_LAUNCHER_PATH": true, "HEADROOM_PACKAGE_VERSION": true, "HEADROOM_READY_NONCE": true, "HEADROOM_MANAGED_SERVICE_RESTART": true, "HEADROOM_PUBLIC_LAUNCHER_PID": true, "HEADROOM_PUBLIC_LAUNCHER_PATH": true, "HEADROOM_PUBLIC_LAUNCHER_TOKEN": true}
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if !managed[upper] && upper != "HEADROOM_APPLY_READY" {
			result = append(result, item)
		}
	}
	for _, key := range []string{"HEADROOM_INSTALL_ROOT", "HEADROOM_LAUNCHER_PATH", "HEADROOM_PACKAGE_VERSION", "HEADROOM_READY_NONCE", "HEADROOM_MANAGED_SERVICE_RESTART"} {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("file is unavailable or oversized")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("file is unavailable or oversized")
	}
	return data, nil
}

func writeDurableJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".headroom-json-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	if err = file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err = replaceAtomic(temporary, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
