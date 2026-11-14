package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCoordinatedReinstall(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("synthetic AppKit wrapper requires native package CI")
	}
	first := activationArchive(t, "42.0.0", "")
	next := activationArchive(t, "42.1.0", "")
	for _, test := range []struct {
		name, archive, mode string
		succeeds            bool
	}{
		{"same version", first, "", true}, {"new version", next, "", true},
		{"busy", next, "HEADROOM_FIXTURE_BUSY", false},
		{"old bridge", next, "HEADROOM_FIXTURE_OLD_BRIDGE", false},
		{"lost reply", next, "HEADROOM_FIXTURE_LOST_REPLY", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, installed := activationInstall(t, first)
			if test.mode != "" {
				t.Setenv(test.mode, "1")
			}
			startActivationPrimary(t, root)
			before, err := os.ReadFile(filepath.Join(root, StateName))
			if err != nil {
				t.Fatal(err)
			}
			result, err := InstallPackage(context.Background(), InstallOptions{Archive: test.archive, InstallRoot: root,
				EntryPath: installed.State.ApplicationEntryPath, Coordinate: true})
			if test.succeeds {
				if err != nil || result.State == nil || result.State.VersionPath == installed.State.VersionPath || result.Activation == nil || result.Activation.Status != "ready" {
					t.Fatalf("reinstall = %+v, %v", result, err)
				}
				data, readErr := os.ReadFile(filepath.Join(root, "last-apply-result.json"))
				var outcome ApplyResult
				if readErr != nil || json.Unmarshal(data, &outcome) != nil || len(outcome.Nonce) != 48 || outcome.VersionPath != result.State.VersionPath {
					t.Fatalf("outcome = %s, %v", data, readErr)
				}
			} else {
				if err == nil {
					t.Fatal("reinstall unexpectedly succeeded")
				}
				if test.mode == "HEADROOM_FIXTURE_LOST_REPLY" {
					stages, readErr := filepath.Glob(filepath.Join(root, "staging", "package-*", "verified-stage.json"))
					if readErr != nil || len(stages) != 1 {
						t.Fatalf("ambiguous dispatch did not retain stage: %v %v", stages, readErr)
					}
					stopActivationFixture(t, root)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					for {
						data, _ := os.ReadFile(filepath.Join(root, "last-apply-result.json"))
						var outcome ApplyResult
						if json.Unmarshal(data, &outcome) == nil && outcome.Status == "applied" {
							break
						}
						if observePause(ctx) != nil {
							t.Fatal("independent committed apply did not finish")
						}
					}
				} else {
					after, readErr := os.ReadFile(filepath.Join(root, StateName))
					if readErr != nil || string(before) != string(after) {
						t.Fatal("rejected reinstall changed state")
					}
				}
			}
			requestCount := 0
			if data, readErr := os.ReadFile(filepath.Join(root, "fixture-reinstall.jsonl")); readErr == nil {
				requestCount = len(strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' }))
			}
			wantRequests := 0
			if test.succeeds || test.mode == "HEADROOM_FIXTURE_LOST_REPLY" {
				wantRequests = 1
			}
			if requestCount != wantRequests {
				t.Fatalf("reinstall requests = %d, want %d", requestCount, wantRequests)
			}
		})
	}
}

func TestReinstallOutcomeCorrelation(t *testing.T) {
	root, installed := activationInstall(t, activationArchive(t, "43.0.0", ""))
	stage, err := StageArchive(activationArchive(t, "43.0.0", ""), root, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := LoadVerifiedStage(root, stageRecordPath(stage))
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := createGeneration(root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	observation := reinstallObservation{root: root, prior: *installed.State, stage: stage}
	request := ApplyRequest{Nonce: strings.Repeat("a", 48), CommitPath: filepath.Join(root, "transactions", "missing", "commit.json")}
	for _, test := range []struct {
		name, nonce, status string
		selected            InstallState
		settled, fails      bool
	}{
		{"matching applied", request.Nonce, "applied", candidate, true, false},
		{"other nonce", strings.Repeat("b", 48), "applied", candidate, false, false},
		{"prior same version result", request.Nonce, "applied", *installed.State, false, true},
		{"rolled back", request.Nonce, "rolled_back", *installed.State, true, true},
		{"recovery required", request.Nonce, "recovery_required", *installed.State, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := writeDurableJSON(filepath.Join(root, StateName), test.selected); err != nil {
				t.Fatal(err)
			}
			if err := writeDurableJSON(filepath.Join(root, "last-apply-result.json"), ApplyResult{Schema: SchemaVersion, Product: "Headroom",
				Nonce: test.nonce, Status: test.status, Version: test.selected.ActiveVersion, VersionPath: test.selected.VersionPath}); err != nil {
				t.Fatal(err)
			}
			settled, result, err := settledReinstallOutcome(observation, request)
			if settled != test.settled || (err != nil) != test.fails || result.State == nil || result.State.VersionPath != test.selected.VersionPath {
				t.Fatalf("settled = %v result = %+v error = %v", settled, result, err)
			}
		})
	}
}

func TestCoordinatedReinstallCLIEntryPaths(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("synthetic AppKit wrapper requires native package CI")
	}
	archive := activationArchive(t, "43.1.0", "")
	for _, test := range []struct {
		name, spelling string
		emptyPrior     bool
		succeeds       bool
	}{
		{"equivalent", "equivalent", false, true},
		{"different", "different", false, false},
		{"both empty", "empty", true, true},
		{"removed", "empty", false, false},
		{"added", "different", true, false},
		{"empty is not dot", ".", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			root := filepath.Join(directory, "install")
			entry := filepath.Join(directory, "headroom-gui"+nativeExtension())
			cliEntry := filepath.Join(directory, "cli", "headroom"+nativeExtension())
			if test.emptyPrior {
				cliEntry = ""
			}
			installed, err := InstallPackage(context.Background(), InstallOptions{Archive: archive, InstallRoot: root, EntryPath: entry, CLIEntryPath: cliEntry})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stopActivationFixture(t, root) })
			startActivationPrimary(t, root)
			requested := test.spelling
			switch test.spelling {
			case "equivalent":
				requested = filepath.Dir(cliEntry) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(cliEntry)
				if runtime.GOOS == "windows" {
					requested = strings.ReplaceAll(strings.ToUpper(requested), `\`, "/")
				}
			case "different":
				requested = filepath.Join(directory, "other", "headroom"+nativeExtension())
			case "empty":
				requested = ""
			}
			before, err := os.ReadFile(filepath.Join(root, StateName))
			if err != nil {
				t.Fatal(err)
			}
			result, err := InstallPackage(context.Background(), InstallOptions{Archive: archive, InstallRoot: root,
				EntryPath: entry, CLIEntryPath: requested, Coordinate: true})
			if test.succeeds {
				if err != nil || result.State == nil || result.State.VersionPath == installed.State.VersionPath || result.Activation == nil || result.Activation.Status != "ready" {
					t.Fatalf("equivalent reinstall = %+v, %v", result, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "cannot change managed entry paths") {
					t.Fatalf("different entry accepted: %+v, %v", result, err)
				}
				after, readErr := os.ReadFile(filepath.Join(root, StateName))
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("rejected entry changed state: %v", readErr)
				}
			}
		})
	}
}

func TestCoordinatedReinstallPriorDigestCheckedUnderLock(t *testing.T) {
	root, installed := activationInstall(t, activationArchive(t, "44.0.0", ""))
	startActivationPrimary(t, root)
	executable, _, err := ActiveExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := probeDesktop(context.Background(), executable, root)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := StageArchive(activationArchive(t, "44.1.0", ""), root, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := installActivationTools(t)
	_, err = PrepareApply(manager, ApplyRequest{InstallRoot: root, EntryPath: installed.State.ApplicationEntryPath,
		StageRecord: stageRecordPath(stage), CurrentPID: info.PID, CurrentExecutable: executable, PriorStateSHA256: strings.Repeat("0", 64)})
	if err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("stale baseline accepted: %v", err)
	}
	transactions, err := filepath.Glob(filepath.Join(root, "transactions", "apply-*"))
	if err != nil || len(transactions) != 0 {
		t.Fatalf("stale baseline created transaction: %v %v", transactions, err)
	}
}

func TestCoordinatedReinstallDamagedRuntimeUsesVerifiedStage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux damaged-runtime fixture")
	}
	archive := activationArchive(t, "46.0.0", "")
	root, installed := activationInstall(t, archive)
	startActivationPrimary(t, root)
	plugin := filepath.Join(root, filepath.FromSlash(installed.State.VersionPath), "plugins", "platforms", "libqxcb.so")
	if err := os.Remove(plugin); err != nil {
		t.Fatal(err)
	}
	result, err := InstallPackage(context.Background(), InstallOptions{Archive: archive, InstallRoot: root, EntryPath: installed.State.ApplicationEntryPath, Coordinate: true})
	if err != nil || result.State == nil || result.State.VersionPath == installed.State.VersionPath || result.Activation != nil {
		t.Fatalf("direct damaged repair = %+v %v", result, err)
	}
	activation, err := ActivateInstall(context.Background(), ActivationOptions{InstallRoot: root, VersionPath: result.State.VersionPath})
	if err != nil || activation.Status != "restart_required" {
		t.Fatalf("repaired activation = %+v %v", activation, err)
	}
}
