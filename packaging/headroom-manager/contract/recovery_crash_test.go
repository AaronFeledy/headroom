package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyCrashRecoveryMatrix(t *testing.T) {
	packages := t.TempDir()
	priorArchive := makePackage(t, packages, "30.0.0", false)
	candidateArchive := makePackage(t, packages, "30.1.0", false)
	for _, point := range []string{
		"prepared", "waiting-for-exit", "current-exited", "stopping-participants", "participants-stopped",
		"copying", "generation-ready", "bootstrap-intent", "bootstrap-applied", "bootstrap-ready",
		"state-intent", "state-applied", "activated", "candidate-launched", "complete", "rolling-back", "rollback-relaunch", "rolled-back", "recovery-rollback-relaunch",
		"rollback-generation-removed", "rollback-generation-only-removed",
		"rollback-app-ready", "recovery-app-ready", "rollback-service-ready", "recovery-service-ready",
	} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			root := filepath.Join(directory, "install")
			entry := filepath.Join(directory, "entry", "headroom"+nativeExtension())
			prior, err := InstallArchive(priorArchive, root, entry, Expectations{})
			if err != nil {
				t.Fatal(err)
			}
			stage, err := StageArchive(candidateArchive, root, Expectations{})
			if err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(directory, "launches.jsonl")
			t.Cleanup(func() { stopCrashFixtures(t, logPath) })
			serviceLog := filepath.Join(directory, "service-launches.jsonl")
			t.Cleanup(func() { stopCrashFixtures(t, serviceLog) })
			t.Cleanup(func() { stopCrashFixtures(t, filepath.Join(directory, "original-service.jsonl")) })
			firstPoint := point
			if strings.HasPrefix(point, "recovery-") {
				firstPoint = "activated"
			}
			environment := []string{"HEADROOM_CRASH_ROOT=" + root, "HEADROOM_CRASH_STAGE=" + stageRecord(stage), "HEADROOM_CRASH_POINT=" + firstPoint}
			if strings.HasSuffix(point, "service-ready") {
				environment = append(environment, "HEADROOM_CRASH_SERVICE=1")
			}
			runCrashHelper(t, environment)
			if strings.HasPrefix(point, "recovery-") {
				recoveryPoint := point
				if point == "recovery-rollback-relaunch" {
					recoveryPoint = "rollback-relaunch"
				}
				runCrashHelper(t, []string{"HEADROOM_CRASH_ROOT=" + root, "HEADROOM_CRASH_POINT=" + recoveryPoint, "HEADROOM_CRASH_RECOVER=1"})
			}
			paths, err := filepath.Glob(filepath.Join(root, "transactions", "apply-*", TransactionJournalName))
			if err != nil || len(paths) != 1 {
				t.Fatalf("crash journal paths = %v, %v", paths, err)
			}
			journal, err := readJournal(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := stopRecordedProcess(journal.Request.CurrentPID, journal.Request.CurrentExecutable, journal.Request.CurrentProcessToken, time.Second); err != nil {
					t.Errorf("stop original fixture: %v", err)
				}
			})
			wantPhase := strings.TrimPrefix(point, "recovery-")
			switch point {
			case "rollback-app-ready", "recovery-app-ready", "rollback-service-ready", "recovery-service-ready":
				wantPhase = "rollback-relaunch"
			case "bootstrap-applied":
				wantPhase = "bootstrap-intent"
			case "state-applied":
				wantPhase = "state-intent"
			case "rollback-generation-removed", "rollback-generation-only-removed":
				wantPhase = "rolling-back"
				if journal.Candidate == nil || journal.GenerationDir == "" {
					t.Fatal("rollback removal crash has no candidate generation")
				}
				if _, err := os.Stat(journal.GenerationDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("crash did not follow candidate generation removal: %v", err)
				}
				if current, err := readStateUnchecked(root); err != nil || current != prior {
					t.Fatalf("removal crash did not leave prior state active: %+v, %v", current, err)
				}
			}
			if string(journal.Phase) != wantPhase {
				t.Fatalf("crashed at %s, journal phase = %s", point, journal.Phase)
			}
			if err = RecoverInstall(root); err != nil {
				t.Fatal(err)
			}
			want := prior
			if point == "complete" {
				want = *journal.Candidate
			}
			assertCrashRecovered(t, root, want)
			if strings.HasSuffix(point, "app-ready") || strings.HasSuffix(point, "service-ready") {
				terminal, err := readJournal(paths[0])
				if err != nil || terminal.Phase != journalPhaseRolledBack {
					t.Fatalf("relaunch recovery did not reach rolled-back: %+v, %v", terminal, err)
				}
				assertPriorLaunchCount(t, logPath, prior)
				if strings.HasSuffix(point, "service-ready") {
					assertPriorLaunchCount(t, serviceLog, prior)
				}
			}
			if err := rejectIncompleteTransactions(root); err != nil {
				t.Errorf("recovery still blocks future updates: %v", err)
			}
			if point != "prepared" && point != "waiting-for-exit" {
				status := "rolled_back"
				if point == "complete" {
					status = "applied"
				}
				assertCrashResult(t, root, want, status)
				if point != "complete" {
					assertPriorRelaunched(t, logPath, prior)
				}
			}
			before := crashRecoverySnapshot(t, root, logPath)
			if err = RecoverInstall(root); err != nil {
				t.Fatal(err)
			}
			after := crashRecoverySnapshot(t, root, logPath)
			if !bytes.Equal(before, after) {
				t.Fatal("second recovery changed state, bootstrap, result, or relaunch evidence")
			}
			if strings.HasSuffix(point, "service-ready") {
				assertPriorLaunchCount(t, serviceLog, prior)
			}
		})
	}
}

func runCrashHelper(t *testing.T, environment []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestRecoveryCrashHelper$")
	command.Env = append(os.Environ(), "HEADROOM_CRASH_HELPER_FIXTURE="+fixtureExecutable)
	command.Env = append(command.Env, "HEADROOM_FIXTURE_SLEEP_MS=60000")
	command.Env = append(command.Env, environment...)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("helper did not abruptly exit at crash point: %v\n%s", err, output)
	}
}

func assertCrashResult(t *testing.T, root string, want InstallState, status string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "last-apply-result.json"))
	var result ApplyResult
	if err != nil || json.Unmarshal(data, &result) != nil || result.Schema != SchemaVersion || result.Product != "Headroom" ||
		result.Status != status || result.Version != want.ActiveVersion || result.VersionPath != want.VersionPath || result.RolledBack != (status == "rolled_back") {
		t.Errorf("recovery result = %s, %v; want %s for %s", data, err, status, want.VersionPath)
	}
}
