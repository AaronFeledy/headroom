package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCoordinatedReinstallPreparedTransactionIsNotRecovered(t *testing.T) {
	archive := activationArchive(t, "47.0.0", "")
	root, installed := activationInstall(t, archive)
	startActivationPrimary(t, root)
	executable, _, err := ActiveExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := probeDesktop(context.Background(), executable, root)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := StageArchive(archive, root, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := installActivationTools(t)
	prepared, err := PrepareApply(manager, ApplyRequest{InstallRoot: root, EntryPath: installed.State.ApplicationEntryPath,
		StageRecord: stageRecordPath(stage), CurrentPID: info.PID, CurrentExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	stopActivationFixture(t, root)
	before, err := os.ReadFile(filepath.Join(root, StateName))
	if err != nil {
		t.Fatal(err)
	}
	_, err = InstallPackage(context.Background(), InstallOptions{Archive: archive, InstallRoot: root, EntryPath: installed.State.ApplicationEntryPath, Coordinate: true})
	if err == nil {
		t.Fatal("reinstall recovered a possibly owned prepared transaction")
	}
	after, err := os.ReadFile(filepath.Join(root, StateName))
	if err != nil || string(before) != string(after) {
		t.Fatal("prepared reinstall changed installed state")
	}
	journal, err := readJournal(filepath.Join(prepared.TransactionDirectory, TransactionJournalName))
	if err != nil || journal.Phase != journalPhasePrepared {
		t.Fatalf("prepared transaction was changed: %+v %v", journal, err)
	}
}
