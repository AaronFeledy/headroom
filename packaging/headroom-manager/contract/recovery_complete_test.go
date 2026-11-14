package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCompletedApplyRecoveryResult(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "install")
	entry := filepath.Join(directory, "entry", "headroom"+nativeExtension())
	prior, err := InstallArchive(makePackage(t, directory, "31.0.0", false), root, entry, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := StageArchive(makePackage(t, directory, "31.1.0", false), root, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "launches.jsonl")
	t.Cleanup(func() { stopCrashFixtures(t, logPath) })
	runCrashHelper(t, []string{"HEADROOM_CRASH_ROOT=" + root, "HEADROOM_CRASH_STAGE=" + stageRecord(stage), "HEADROOM_CRASH_POINT=complete"})
	paths, err := filepath.Glob(filepath.Join(root, "transactions", "apply-*", TransactionJournalName))
	if err != nil || len(paths) != 1 {
		t.Fatalf("complete journal paths = %v, %v", paths, err)
	}
	journal, err := readJournal(paths[0])
	if err != nil || journal.Phase != journalPhaseComplete || journal.Candidate == nil {
		t.Fatalf("complete crash journal = %+v, %v", journal, err)
	}
	candidate := *journal.Candidate
	bound := ApplyResult{Nonce: journal.Request.Nonce, Schema: SchemaVersion, Product: "Headroom", Status: "applied", Version: candidate.ActiveVersion, VersionPath: candidate.VersionPath}
	for _, name := range []string{"missing", "malformed", "wrong-version", "wrong-generation", "wrong-nonce", "wrong-status", "bound", "prior-state", "write-failure"} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(paths[0]), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := writeDurableJSON(paths[0], journal); err != nil {
				t.Fatal(err)
			}
			if err := writeDurableJSON(filepath.Join(root, StateName), candidate); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(root, "last-apply-result.json")
			if err := os.Remove(resultPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			result := bound
			var before []byte
			switch name {
			case "malformed":
				before = []byte("{not-json")
			case "wrong-version":
				result.Version = prior.ActiveVersion
			case "wrong-generation":
				result.VersionPath = prior.VersionPath
			case "wrong-nonce":
				result.Nonce = "000000000000000000000000000000000000000000000000"
			case "wrong-status":
				result.Status, result.RolledBack = "rolled_back", true
			case "bound":
				before, err = json.MarshalIndent(result, "", "    ")
				if err != nil {
					t.Fatal(err)
				}
			case "prior-state":
				if err := writeDurableJSON(filepath.Join(root, StateName), prior); err != nil {
					t.Fatal(err)
				}
			case "write-failure":
				if err := os.Mkdir(resultPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "malformed", "bound":
				if err := os.WriteFile(resultPath, before, 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong-version", "wrong-generation", "wrong-nonce", "wrong-status":
				if err := writeDurableJSON(resultPath, result); err != nil {
					t.Fatal(err)
				}
			}
			err := RecoverInstall(root)
			if name == "write-failure" {
				if err == nil {
					t.Fatal("result persistence failure was swallowed")
				}
				remaining, readErr := readJournal(paths[0])
				if readErr != nil || remaining.Phase != journalPhaseComplete {
					t.Fatalf("completed recovery evidence lost on write failure: %+v, %v", remaining, readErr)
				}
				if err := os.Remove(resultPath); err != nil {
					t.Fatal(err)
				}
				err = RecoverInstall(root)
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "prior-state" {
				if _, err := os.Stat(resultPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery invented candidate success for prior state: %v", err)
				}
			} else {
				assertCrashResult(t, root, candidate, "applied")
				data, readErr := readBoundedFile(resultPath, maxManifestBytes)
				var persisted ApplyResult
				if readErr != nil || json.Unmarshal(data, &persisted) != nil {
					t.Fatalf("read recovered outcome: %s, %v", data, readErr)
				}
				if persisted.Nonce != journal.Request.Nonce {
					t.Fatalf("recovered nonce = %q, want %q", persisted.Nonce, journal.Request.Nonce)
				}
			}
			if name == "bound" {
				after, err := os.ReadFile(resultPath)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("bound result was rewritten: %q, %v", after, err)
				}
			}
			if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completed transaction was not removed: %v", err)
			}
		})
	}
}
