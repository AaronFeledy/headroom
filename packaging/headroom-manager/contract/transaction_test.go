package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTransactionalApplyAndReadinessUseActualGeneration(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "10.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	inspection := InspectInstall(installRoot)
	ownedPath := installedTestComponent(t, installRoot, inspection.VersionPath, true)
	owned := startFixturePath(t, ownedPath)
	unrelatedPath := filepath.Join(root, "unrelated", "bin", "usage-server"+nativeExtension())
	if err := os.MkdirAll(filepath.Dir(unrelatedPath), 0755); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, fixtureExecutable, unrelatedPath)
	unrelated := startFixturePath(t, unrelatedPath)
	unrelatedToken, err := captureProcessToken(unrelated.Process.Pid, unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _, _ = unrelated.Process.Wait() }()
	second := makePackage(t, root, "10.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		OwnedChildPID: owned.Process.Pid, OwnedChildExecutable: ownedPath,
		RelaunchArguments: []string{"--background"}, WaitTimeoutMS: 3000, ReadyTimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	resultChannel := make(chan struct {
		result ApplyResult
		err    error
	}, 1)
	go func() {
		result, applyErr := ApplyPrepared(prepared.RequestPath)
		resultChannel <- struct {
			result ApplyResult
			err    error
		}{result, applyErr}
	}()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	_ = owned.Process.Kill()
	_, _ = owned.Process.Wait()
	outcome := <-resultChannel
	if outcome.err != nil || outcome.result.Status != "applied" || outcome.result.Version != "10.1.0" {
		t.Fatalf("apply outcome = %+v, %v", outcome.result, outcome.err)
	}
	inspection = InspectInstall(installRoot)
	if !inspection.Complete || inspection.Version != "10.1.0" || !strings.Contains(inspection.VersionPath, ".generation-") {
		t.Fatalf("active generation = %+v", inspection)
	}
	journal, err := readJournal(filepath.Join(prepared.TransactionDirectory, TransactionJournalName))
	if err != nil || journal.CandidatePID <= 0 {
		t.Fatalf("candidate identity missing: %+v %v", journal, err)
	}
	if err = stopRecordedProcess(journal.CandidatePID, journal.CandidateExe, journal.CandidateToken, time.Second); err != nil {
		t.Fatal(err)
	}
	if token, tokenErr := captureProcessToken(unrelated.Process.Pid, unrelatedPath); tokenErr != nil || token != unrelatedToken {
		t.Fatalf("unrelated process was disturbed: %q %v", token, tokenErr)
	}
}

func TestPrepareApplyCapturesRoleBasedAdditionalProcessIdentity(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "10.2.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	inspection := InspectInstall(installRoot)
	serverPath := installedTestComponent(t, installRoot, inspection.VersionPath, true)
	applicationPath := installedTestComponent(t, installRoot, inspection.VersionPath, false)
	server := startFixturePath(t, serverPath)
	application := startFixturePath(t, applicationPath)
	defer func() {
		_ = server.Process.Kill()
		_, _ = server.Process.Wait()
		_ = application.Process.Kill()
		_, _ = application.Process.Wait()
	}()
	second := makePackage(t, root, "10.3.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{
		InstallRoot: installRoot, EntryPath: entry, StageRecord: stageRecord(stage),
		CurrentPID: server.Process.Pid, CurrentExecutable: serverPath, CurrentRole: RoleServer, CandidateRole: RoleServer,
		AdditionalProcesses: []ProcessClaim{{Role: RoleApplication, PID: application.Process.Pid, Executable: applicationPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := readApplyRequest(prepared.RequestPath)
	if err != nil {
		t.Fatal(err)
	}
	if request.CurrentRole != RoleServer || request.CandidateRole != RoleServer || request.CurrentProcessToken == "" || len(request.AdditionalProcesses) != 1 || request.AdditionalProcesses[0].ProcessToken == "" {
		t.Fatalf("captured request = %+v", request)
	}
}

func TestPrepareApplyRejectsDowngradeStage(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	currentArchive := makePackage(t, root, "10.4.0", false)
	if _, err := InstallArchive(currentArchive, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	defer func() { _ = current.Process.Kill(); _, _ = current.Process.Wait() }()
	downgrade := makePackage(t, root, "10.3.0", false)
	stage, err := StageArchive(downgrade, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry, StageRecord: stageRecord(stage),
		CurrentPID: current.Process.Pid, CurrentExecutable: current.Path})
	if err == nil || !strings.Contains(err.Error(), "version direction") {
		t.Fatalf("downgrade prepare result = %v", err)
	}
}

func TestMigrateManagedEntriesReplacesOnlyTrustedLegacyEntry(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	applicationEntry := filepath.Join(root, "internal", "headroom-gui"+nativeExtension())
	publicCLI := filepath.Join(root, "public", "headroom"+nativeExtension())
	archive := makePackage(t, root, "10.4.0", false)
	if _, err := InstallArchive(archive, installRoot, applicationEntry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(applicationEntry, publicCLI, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBytes([]byte(installRoot+"\n"), publicCLI+".root", 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := MigrateManagedEntries(installRoot, applicationEntry, publicCLI, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.ApplicationEntryPath != applicationEntry || state.CLIEntryPath != publicCLI {
		t.Fatalf("migration state = %+v", state)
	}
	if runtimeWindows() {
		if state.ServerEntryPath != "" {
			t.Fatalf("Windows server compatibility entry = %q", state.ServerEntryPath)
		}
	} else if state.ServerEntryPath != filepath.Join(filepath.Dir(publicCLI), "usage-server") {
		t.Fatalf("server compatibility entry = %q", state.ServerEntryPath)
	}
	if active, _, activeErr := ActiveExecutableForRole(installRoot, RolePublicLauncher); activeErr != nil || !samePath(active, publicCLI) {
		t.Fatalf("public launcher = %q, %v", active, activeErr)
	}
	if inspection := InspectInstall(installRoot); !inspection.Complete || !inspection.TrustedIdentity {
		t.Fatalf("migration inspection = %+v", inspection)
	}
}

func TestApplyStopsAndRestartsExactManagedService(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom-gui"+nativeExtension())
	cliEntry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "10.5.0", false)
	if _, err := InstallArchiveWithCLIEntry(first, installRoot, entry, cliEntry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	cliPath, _, err := ActiveExecutableForRole(installRoot, RoleCLI)
	if err != nil {
		t.Fatal(err)
	}
	service := startFixturePath(t, cliPath)
	serviceToken, err := captureProcessToken(service.Process.Pid, cliPath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(root, "managed-service-evidence")
	if err = writeManagedService(installRoot, ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: installRoot,
		PID: service.Process.Pid, Executable: cliPath, ProcessToken: serviceToken, Arguments: []string{"--config", "fixture.yaml"}, Owner: owner,
		Environment: []string{"USAGE_CONFIG=" + evidence, "USAGE_LISTEN_ADDR=127.0.0.1:17823", "USAGE_PROVIDER_CODEX_ENABLED=false"}, WorkingDirectory: root, Ready: true}); err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "10.6.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry, StageRecord: stageRecord(stage),
		CurrentPID: current.Process.Pid, CurrentExecutable: current.Path, WaitTimeoutMS: 3000, ReadyTimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := readApplyRequest(prepared.RequestPath)
	if err != nil || len(request.AdditionalProcesses) != 1 || request.AdditionalProcesses[0].Role != RoleManagedServer {
		t.Fatalf("managed request = %+v, %v", request, err)
	}
	done := make(chan error, 1)
	go func() { _, applyErr := ApplyPrepared(prepared.RequestPath); done <- applyErr }()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, waitErr := service.Process.Wait(); waitErr != nil {
		t.Fatalf("managed service was not reaped: %v", waitErr)
	}
	restarted, err := ReadManagedService(installRoot)
	if err != nil || !strings.Contains(restarted.Executable, "10.6.0.generation-") || !reflect.DeepEqual(restarted.Arguments, []string{"--config", "fixture.yaml"}) ||
		!reflect.DeepEqual(restarted.Environment, []string{"USAGE_CONFIG=" + evidence, "USAGE_LISTEN_ADDR=127.0.0.1:17823", "USAGE_PROVIDER_CODEX_ENABLED=false"}) || restarted.WorkingDirectory != root {
		t.Fatalf("restarted service = %+v, %v", restarted, err)
	}
	if data, readErr := os.ReadFile(evidence); readErr != nil || string(data) != root+"\n127.0.0.1:17823\n" {
		t.Fatalf("managed service environment/cwd evidence = %q, %v", data, readErr)
	}
	journal, _ := readJournal(filepath.Join(prepared.TransactionDirectory, TransactionJournalName))
	_ = stopRecordedProcess(journal.CandidatePID, journal.CandidateExe, journal.CandidateToken, time.Second)
	_ = stopRecordedProcess(restarted.PID, restarted.Executable, restarted.ProcessToken, time.Second)
}

func TestRecoveryFinishesInFlightManagedServiceStop(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	service := startFixturePath(t, fixtureExecutable)
	token, err := captureProcessToken(service.Process.Pid, fixtureExecutable)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = writeManagedService(root, ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root,
		PID: service.Process.Pid, Executable: fixtureExecutable, ProcessToken: token, Owner: owner, WorkingDirectory: root, Ready: true}); err != nil {
		t.Fatal(err)
	}
	request := ApplyRequest{AdditionalProcesses: []ProcessClaim{{Role: RoleManagedServer, PID: service.Process.Pid, Executable: fixtureExecutable, ProcessToken: token}}}
	if err = finishManagedServiceStopForRecovery(root, request); err != nil {
		t.Fatal(err)
	}
	_, _ = service.Process.Wait()
	if !processGone(service.Process.Pid) {
		t.Fatal("managed service remained alive after stop-intent recovery")
	}
	if _, err = os.Stat(filepath.Join(root, "runtime", ManagedServiceRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale managed service receipt remains: %v", err)
	}
}

func TestTransactionalApplyRollsBackAndReopensOnReadinessFailure(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	t.Setenv("HEADROOM_FIXTURE_NO_READY_VERSION", "11.1.0")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "11.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	second := makePackage(t, root, "11.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		WaitTimeoutMS: 3000, ReadyTimeoutMS: 300})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, applyErr := ApplyPrepared(prepared.RequestPath); done <- applyErr }()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	rollbackReady := filepath.Join(prepared.TransactionDirectory, "rollback-ready.json")
	// This observer covers package verification, file replacement, and rollback
	// as well as the deliberate readiness timeout. Loaded Windows runners can
	// take more than two seconds before the rollback process is even launched.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(rollbackReady); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rollback relaunch did not reach readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if competing, lockErr := acquireInstallLock(installRoot, 40*time.Millisecond); lockErr == nil {
		competing.Close()
		t.Fatal("competing install acquired lock during rollback relaunch")
	}
	if err = <-done; err == nil || !strings.Contains(err.Error(), "readiness") {
		t.Fatalf("readiness failure = %v", err)
	}
	inspection := InspectInstall(installRoot)
	if inspection.Version != "11.0.0" || inspection.ApplyStatus != "rolled_back" {
		t.Fatalf("rollback inspection = %+v", inspection)
	}
	readyData, err := readBoundedFile(rollbackReady, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		PID        int    `json:"pid"`
		Executable string `json:"executable"`
	}
	if json.Unmarshal(readyData, &ready) != nil || ready.PID <= 0 {
		t.Fatalf("rollback readiness = %s", readyData)
	}
	token, err := captureProcessToken(ready.PID, ready.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = stopRecordedProcess(ready.PID, ready.Executable, token, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionalApplyRollsBackAfterCandidateCrash(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	t.Setenv("HEADROOM_FIXTURE_CRASH_VERSION", "11.3.0")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "11.2.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	second := makePackage(t, root, "11.3.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		WaitTimeoutMS: 3000, ReadyTimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, applyErr := ApplyPrepared(prepared.RequestPath); done <- applyErr }()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	if err = <-done; err == nil || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("candidate crash = %v", err)
	}
	inspection := InspectInstall(installRoot)
	if inspection.Version != "11.2.0" || inspection.ApplyStatus != "rolled_back" {
		t.Fatalf("crash rollback inspection = %+v", inspection)
	}
	readyData, err := readBoundedFile(filepath.Join(prepared.TransactionDirectory, "rollback-ready.json"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		PID        int    `json:"pid"`
		Executable string `json:"executable"`
	}
	if json.Unmarshal(readyData, &ready) != nil || ready.PID <= 0 {
		t.Fatalf("rollback readiness = %s", readyData)
	}
	token, err := captureProcessToken(ready.PID, ready.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = stopRecordedProcess(ready.PID, ready.Executable, token, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsStaleStateBeforeAcknowledgement(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	archive := makePackage(t, root, "12.0.0", false)
	if _, err := InstallArchive(archive, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	defer func() { _ = current.Process.Kill(); _, _ = current.Process.Wait() }()
	stage, err := StageArchive(archive, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(installRoot, StateName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyPrepared(prepared.RequestPath); err == nil || !strings.Contains(err.Error(), "changed before apply acknowledgement") {
		t.Fatalf("stale state apply = %v", err)
	}
	if _, err = os.Stat(prepared.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("stale request was acknowledged: %v", err)
	}
}

func TestAcknowledgedApplyWithoutCommitExpiresWhileCurrentKeepsRunning(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "12.1.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	defer func() { _ = current.Process.Kill(); _, _ = current.Process.Wait() }()
	currentToken, err := captureProcessToken(current.Process.Pid, current.Path)
	if err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "12.2.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path, CommitTimeoutMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, applyErr := ApplyPrepared(prepared.RequestPath); done <- applyErr }()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil || !strings.Contains(err.Error(), "not committed") {
		t.Fatalf("uncommitted apply = %v", err)
	}
	if token, tokenErr := captureProcessToken(current.Process.Pid, current.Path); tokenErr != nil || token != currentToken {
		t.Fatalf("current app changed after abandoned prepare: %q %v", token, tokenErr)
	}
	if inspection := InspectInstall(installRoot); inspection.Version != "12.1.0" || !inspection.Complete {
		t.Fatalf("uncommitted apply changed installation: %+v", inspection)
	}
}

func TestPrepareRejectsSecondIncompleteApplyTransaction(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "12.3.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	defer func() { _ = current.Process.Kill(); _, _ = current.Process.Wait() }()
	second := makePackage(t, root, "12.4.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	request := ApplyRequest{InstallRoot: installRoot, EntryPath: entry, StageRecord: stageRecord(stage),
		CurrentPID: current.Process.Pid, CurrentExecutable: current.Path}
	if _, err = PrepareApply(fixtureExecutable, request); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareApply(fixtureExecutable, request); err == nil || !strings.Contains(err.Error(), "must be recovered") {
		t.Fatalf("second incomplete apply = %v", err)
	}
}

func TestExternalInstallReportsBootstrapRecoveryFailure(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "12.5.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	transactionTestHook = func(phase string) error {
		if phase != "install-bootstrap-intent" {
			return nil
		}
		directories, _ := filepath.Glob(filepath.Join(installRoot, "transactions", "install-*"))
		if len(directories) != 1 {
			return errors.New("synthetic install interruption has no journal")
		}
		if err := os.Remove(filepath.Join(directories[0], "backup-0")); err != nil {
			return err
		}
		return errors.New("synthetic install interruption")
	}
	defer func() { transactionTestHook = nil }()
	second := makePackage(t, root, "12.6.0", false)
	_, err := InstallArchive(second, installRoot, entry, Expectations{})
	if err == nil || !strings.Contains(err.Error(), "synthetic install interruption") || !strings.Contains(err.Error(), "backup-0") {
		t.Fatalf("external recovery failure = %v", err)
	}
	if inspection := InspectInstall(installRoot); inspection.Version != "12.5.0" || !inspection.Complete {
		t.Fatalf("failed external install changed active state: %+v", inspection)
	}
}

func TestExternalInstallRecoveryRestoresStateAndPartlyReplacedBootstrap(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "13.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(installRoot, "headroom-launcher")
	if runtimeWindows() {
		launcher = filepath.Join(installRoot, "headroom.exe")
	}
	priorLauncher, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "13.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stage, manifest, err := LoadVerifiedStage(installRoot, stageRecord(stage))
	if err != nil {
		t.Fatal(err)
	}
	generationState, generation, err := createGeneration(installRoot, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	transactions := filepath.Join(installRoot, "transactions")
	directory := filepath.Join(transactions, "install-0123456789abcdef")
	if err = os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = copyFile(filepath.Join(installRoot, StateName), filepath.Join(directory, "prior-install-state.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	priorStateHash, err := digestFile(filepath.Join(installRoot, StateName))
	if err != nil {
		t.Fatal(err)
	}
	journal := InstallJournal{Schema: SchemaVersion, Product: "Headroom", Phase: "bootstrap-intent", InstallRoot: installRoot,
		EntryPath: entry, GenerationDir: generation, Candidate: generationState, PriorStateExisted: true, PriorStateSHA256: priorStateHash}
	if err = writeDurableJSON(filepath.Join(directory, InstallJournalName), journal); err != nil {
		t.Fatal(err)
	}
	if err = prepareBootstrapBackup(installRoot, entry, directory); err != nil {
		t.Fatal(err)
	}
	if err = replaceBytes([]byte("interrupted replacement"), launcher, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = RecoverInstall(installRoot); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(launcher)
	if err != nil || string(restored) != string(priorLauncher) {
		t.Fatalf("stable launcher was not restored: %v", err)
	}
	if inspection := InspectInstall(installRoot); inspection.Version != "13.0.0" || !inspection.Complete {
		t.Fatalf("external recovery inspection = %+v", inspection)
	}
	if _, err = os.Stat(generation); !os.IsNotExist(err) {
		t.Fatalf("interrupted generation survived: %v", err)
	}
}

func TestRecoveryStopsExactOrphanCandidateBeforeRollback(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "14.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	previous, err := readTrustedState(installRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	priorStateHash, err := digestFile(filepath.Join(installRoot, StateName))
	if err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "14.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stage, manifest, err := LoadVerifiedStage(installRoot, stageRecord(stage))
	if err != nil {
		t.Fatal(err)
	}
	candidate, generation, err := createGeneration(installRoot, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(installRoot, "transactions", "apply-0123456789abcdef0123456789abcdef")
	if err = os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = prepareBootstrapBackup(installRoot, entry, directory); err != nil {
		t.Fatal(err)
	}
	if err = applyBootstrap(stage.PackageRoot, installRoot, entry, manifest); err != nil {
		t.Fatal(err)
	}
	if err = writeDurableJSON(filepath.Join(installRoot, StateName), candidate); err != nil {
		t.Fatal(err)
	}
	candidateExecutable := installedTestComponent(t, installRoot, candidate.VersionPath, false)
	process := startFixturePath(t, candidateExecutable)
	token, err := captureProcessToken(process.Process.Pid, candidateExecutable)
	if err != nil {
		t.Fatal(err)
	}
	request := ApplyRequest{Schema: SchemaVersion, Product: "Headroom", InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: 1, CurrentExecutable: installedTestComponent(t, installRoot, previous.VersionPath, false),
		CurrentProcessToken: "recorded-prior-token", PriorStateSHA256: priorStateHash, AcknowledgementPath: filepath.Join(directory, "accepted.json"),
		CommitPath: filepath.Join(directory, "commit.json"), ReadyPath: filepath.Join(directory, "ready.json"), Nonce: strings.Repeat("a", 48),
		WaitTimeoutMS: 1000, CommitTimeoutMS: 1000, ReadyTimeoutMS: 1000}
	journal := ApplyJournal{Schema: SchemaVersion, Product: "Headroom", Phase: "candidate-launched", Request: request,
		Previous: previous, Candidate: &candidate, GenerationDir: generation, CandidatePID: process.Process.Pid,
		CandidateExe: candidateExecutable, CandidateToken: token}
	if err = writeDurableJSON(filepath.Join(directory, TransactionJournalName), journal); err != nil {
		t.Fatal(err)
	}
	if err = RecoverInstall(installRoot); err != nil {
		t.Fatal(err)
	}
	recoveryReady, err := readBoundedFile(filepath.Join(directory, "recovery-ready.json"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	var relaunched struct {
		PID        int    `json:"pid"`
		Executable string `json:"executable"`
	}
	if json.Unmarshal(recoveryReady, &relaunched) != nil || relaunched.PID <= 0 {
		t.Fatalf("recovery readiness = %s", recoveryReady)
	}
	relaunchedToken, err := captureProcessToken(relaunched.PID, relaunched.Executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := stopRecordedProcess(relaunched.PID, relaunched.Executable, relaunchedToken, time.Second); stopErr != nil {
			t.Errorf("stop recovered prior application: %v", stopErr)
		}
	})
	_, _ = process.Process.Wait()
	if !processGone(process.Process.Pid) {
		t.Fatal("orphan candidate survived recovery")
	}
	if inspection := InspectInstall(installRoot); inspection.Version != "14.0.0" || inspection.ApplyStatus != "rolled_back" {
		t.Fatalf("orphan recovery = %+v", inspection)
	}
	if _, err = os.Stat(generation); !os.IsNotExist(err) {
		t.Fatalf("orphan generation survived rollback: %v", err)
	}
}

func TestRollbackRestoreFailureIsRecoveryRequiredAndPreservesGeneration(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "15.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	second := makePackage(t, root, "15.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		WaitTimeoutMS: 3000, ReadyTimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	transactionTestHook = func(phase string) error {
		if phase == "candidate-launched" {
			_ = os.Remove(filepath.Join(prepared.TransactionDirectory, "backup-0"))
			return errors.New("synthetic post-launch failure")
		}
		return nil
	}
	defer func() { transactionTestHook = nil }()
	done := make(chan struct {
		result ApplyResult
		err    error
	}, 1)
	go func() {
		result, applyErr := ApplyPrepared(prepared.RequestPath)
		done <- struct {
			result ApplyResult
			err    error
		}{result, applyErr}
	}()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	outcome := <-done
	if outcome.err == nil || outcome.result.Status != "recovery_required" || outcome.result.RolledBack {
		t.Fatalf("restore failure outcome = %+v, %v", outcome.result, outcome.err)
	}
	inspection := InspectInstall(installRoot)
	if inspection.Version != "15.1.0" || inspection.ApplyStatus != "recovery_required" {
		t.Fatalf("mixed recovery state was mislabeled: %+v", inspection)
	}
	if _, statErr := os.Stat(filepath.Join(installRoot, filepath.FromSlash(inspection.VersionPath))); statErr != nil {
		t.Fatalf("recoverable candidate generation was removed: %v", statErr)
	}
	oldJournal := filepath.Join(prepared.TransactionDirectory, TransactionJournalName)
	transactionTestHook = nil
	third := makePackage(t, root, "15.2.0", false)
	transactionTestHook = func(phase string) error {
		if phase == "install-bootstrap-intent" {
			return errors.New("synthetic superseding install failure")
		}
		return nil
	}
	if _, err = InstallArchive(third, installRoot, entry, Expectations{}); err == nil {
		t.Fatal("synthetic superseding install unexpectedly succeeded")
	}
	if _, err = os.Stat(oldJournal); err != nil {
		t.Fatalf("failed superseding install retired the prior recovery journal: %v", err)
	}
	transactionTestHook = nil
	if _, err = InstallArchive(third, installRoot, entry, Expectations{}); err != nil {
		t.Fatalf("verified external reinstall could not supersede unrecoverable transaction: %v", err)
	}
	inspection = InspectInstall(installRoot)
	if inspection.Version != "15.2.0" || !inspection.Complete || inspection.ApplyStatus != "" {
		t.Fatalf("superseding reinstall = %+v", inspection)
	}
}

func TestRollbackGenerationRemovalFailureIsNotLabeledRolledBack(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	t.Setenv("HEADROOM_FIXTURE_NO_READY_VERSION", "15.4.0")
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "15.3.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	current := startInstalledFixture(t, installRoot)
	second := makePackage(t, root, "15.4.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{InstallRoot: installRoot, EntryPath: entry,
		StageRecord: stageRecord(stage), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		WaitTimeoutMS: 3000, ReadyTimeoutMS: 200})
	if err != nil {
		t.Fatal(err)
	}
	transactionTestHook = func(phase string) error {
		if phase == "rollback-remove-generation" {
			return errors.New("synthetic generation removal failure")
		}
		return nil
	}
	defer func() { transactionTestHook = nil }()
	done := make(chan struct {
		result ApplyResult
		err    error
	}, 1)
	go func() {
		result, applyErr := ApplyPrepared(prepared.RequestPath)
		done <- struct {
			result ApplyResult
			err    error
		}{result, applyErr}
	}()
	if err = WaitForApplyAcknowledgement(prepared, time.Second); err != nil {
		t.Fatal(err)
	}
	authorizePreparedApply(t, prepared)
	_ = current.Process.Kill()
	_, _ = current.Process.Wait()
	outcome := <-done
	if outcome.err == nil || outcome.result.Status != "recovery_required" || outcome.result.RolledBack ||
		!strings.Contains(outcome.err.Error(), "generation removal") {
		t.Fatalf("generation cleanup failure = %+v, %v", outcome.result, outcome.err)
	}
	journal, err := readJournal(filepath.Join(prepared.TransactionDirectory, TransactionJournalName))
	if err != nil || journal.GenerationDir == "" {
		t.Fatalf("cleanup journal = %+v %v", journal, err)
	}
	if _, err = os.Stat(journal.GenerationDir); err != nil {
		t.Fatalf("failed-cleanup generation was not retained: %v", err)
	}
}

func TestInstallLockRejectsLink(t *testing.T) {
	if runtimeWindows() {
		t.Skip("native Windows reparse coverage runs in package CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "foreign-lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(installRoot, "transaction.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstallLock(installRoot, time.Millisecond); err == nil {
		t.Fatal("linked transaction lock was accepted")
	}
}

func TestBootstrapTamperAtSwitchIsRejectedAndRestorable(t *testing.T) {
	root := t.TempDir()
	installRoot := filepath.Join(root, "install")
	entry := filepath.Join(root, "entry", "headroom"+nativeExtension())
	first := makePackage(t, root, "16.0.0", false)
	if _, err := InstallArchive(first, installRoot, entry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(installRoot, "headroom-launcher")
	if runtimeWindows() {
		launcher = filepath.Join(installRoot, "headroom.exe")
	}
	want, err := digestFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	second := makePackage(t, root, "16.1.0", false)
	stage, err := StageArchive(second, installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stage, manifest, err := LoadVerifiedStage(installRoot, stageRecord(stage))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(installRoot, "transactions", "install-fedcba9876543210")
	if err = os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = prepareBootstrapBackup(installRoot, entry, directory); err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(stage.PackageRoot, "bootstrap", "headroom"+nativeExtension())
	data, err := os.ReadFile(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err = os.WriteFile(bootstrap, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = applyBootstrap(stage.PackageRoot, installRoot, entry, manifest); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered bootstrap switch = %v", err)
	}
	if err = restoreBootstrap(installRoot, entry, directory); err != nil {
		t.Fatal(err)
	}
	got, err := digestFile(launcher)
	if err != nil || got != want {
		t.Fatalf("stable launcher restore = %q %v", got, err)
	}
}

func TestProcessWatchRejectsCreationTokenMismatchWithoutStoppingProcess(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	process := startFixturePath(t, fixtureExecutable)
	defer func() { _ = process.Process.Kill(); _, _ = process.Process.Wait() }()
	token, err := captureProcessToken(process.Process.Pid, fixtureExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = watchProcess(process.Process.Pid, fixtureExecutable, token+"changed"); err == nil {
		t.Fatal("changed process creation token was accepted")
	}
	if got, captureErr := captureProcessToken(process.Process.Pid, fixtureExecutable); captureErr != nil || got != token {
		t.Fatalf("mismatched watch disturbed process: %q %v", got, captureErr)
	}
}

func stageRecord(stage StageResult) string {
	return filepath.Join(filepath.Dir(filepath.Dir(stage.PackageRoot)), "verified-stage.json")
}

func authorizePreparedApply(t *testing.T, prepared PreparedApply) {
	t.Helper()
	if err := writeDurableJSON(prepared.CommitPath, map[string]any{"schema": SchemaVersion, "product": "Headroom", "commit": true, "nonce": prepared.Nonce}); err != nil {
		t.Fatal(err)
	}
}

func startInstalledFixture(t *testing.T, root string) *exec.Cmd {
	t.Helper()
	executable, _, err := ActiveExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	return startFixturePath(t, executable)
}

func startFixturePath(t *testing.T, executable string) *exec.Cmd {
	t.Helper()
	command := exec.Command(executable)
	command.Env = os.Environ()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Windows cannot remove a running executable, so reap fixtures before t.TempDir cleanup runs.
	t.Cleanup(func() {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		_ = command.Wait() // A killed or already-reaped fixture's exit status is irrelevant.
	})
	return command
}

// A CLI-only install passes the same path as the application entry and the CLI
// entry. applyBootstrapWithCLI still replaces the usage-server compatibility
// entry in that shape, so the rollback target list has to cover it too.
func TestBootstrapRollbackCoversSharedCLIEntryServerCompatibilityPath(t *testing.T) {
	if runtimeWindows() {
		t.Skip("the usage-server compatibility entry exists only on Linux and macOS")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "bin", "headroom")
	targets := stableBootstrapTargetsWithCLI(root, entry, entry)
	server := filepath.Join(filepath.Dir(entry), "usage-server")
	for _, wanted := range []string{entry, entry + ".root", server, server + ".root"} {
		found := false
		for _, target := range targets {
			if samePath(target, wanted) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rollback targets omitted %q, which apply replaces: %v", wanted, targets)
		}
	}
	counts := map[string]int{}
	for _, target := range targets {
		counts[filepath.Clean(target)]++
	}
	for target, count := range counts {
		if count != 1 {
			t.Fatalf("rollback target %q appears %d times, want 1", target, count)
		}
	}
}
