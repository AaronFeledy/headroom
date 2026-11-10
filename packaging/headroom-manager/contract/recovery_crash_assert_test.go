package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type crashLaunch struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	Version    string `json:"version"`
}

func crashLaunches(t *testing.T, path string) []crashLaunch {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var launches []crashLaunch
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var launch crashLaunch
		if err := json.Unmarshal(line, &launch); err != nil {
			t.Fatal(err)
		}
		launches = append(launches, launch)
	}
	return launches
}

func stopCrashFixtures(t *testing.T, logPath string) {
	t.Helper()
	for _, launch := range crashLaunches(t, logPath) {
		token, err := captureProcessToken(launch.PID, launch.Executable)
		if err != nil && processGone(launch.PID) {
			continue
		}
		if err != nil {
			t.Errorf("capture fixture identity: %v", err)
			continue
		}
		if err := stopRecordedProcess(launch.PID, launch.Executable, token, time.Second); err != nil {
			t.Errorf("stop fixture: %v", err)
		}
	}
}

func assertPriorRelaunched(t *testing.T, logPath string, prior InstallState) {
	t.Helper()
	launches := crashLaunches(t, logPath)
	if len(launches) == 0 {
		t.Fatal("prior app was not relaunched")
	}
	last := launches[len(launches)-1]
	if last.Version != prior.ActiveVersion || last.PID <= 0 {
		t.Fatalf("last app launch = %+v; want prior %s", last, prior.ActiveVersion)
	}
	if _, err := captureProcessToken(last.PID, last.Executable); err != nil {
		t.Fatalf("relaunched prior is not running: %v", err)
	}
}

func assertPriorLaunchCount(t *testing.T, logPath string, prior InstallState) {
	t.Helper()
	count := 0
	for _, launch := range crashLaunches(t, logPath) {
		if launch.Version == prior.ActiveVersion {
			count++
		}
	}
	if count != 1 {
		t.Errorf("prior launches in %s = %d; want 1", filepath.Base(logPath), count)
	}
}

func assertCrashRecovered(t *testing.T, root string, want InstallState) {
	t.Helper()
	state, err := readStateUnchecked(root)
	if err != nil || state != want {
		t.Fatalf("recovered state = %+v, %v; want %+v", state, err, want)
	}
	inspection := InspectInstall(root)
	if !inspection.TrustedIdentity || !inspection.Complete {
		t.Fatalf("recovered install is not trusted and complete: %+v", inspection)
	}
	if _, _, err := ActiveExecutable(root); err != nil {
		t.Fatalf("recovered install is not launchable: %v", err)
	}
	manifest, err := installedManifest(root, want)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledBootstrap(root, want.ApplicationEntryPath, manifest); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "transactions", "apply-*", TransactionJournalName))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		journal, err := readJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		switch journal.Phase {
		case journalPhaseComplete, journalPhaseRolledBack, journalPhaseObsolete:
		default:
			t.Errorf("nonterminal apply journal remains: %s", journal.Phase)
		}
	}
}

func crashRecoverySnapshot(t *testing.T, root, logPath string) []byte {
	t.Helper()
	state, err := readStateUnchecked(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := append(stableBootstrapTargetsWithCLI(root, state.ApplicationEntryPath, state.CLIEntryPath),
		filepath.Join(root, StateName), filepath.Join(root, "last-apply-result.json"), logPath)
	var snapshot []byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		snapshot = append(snapshot, data...)
		snapshot = append(snapshot, 0)
	}
	return snapshot
}
