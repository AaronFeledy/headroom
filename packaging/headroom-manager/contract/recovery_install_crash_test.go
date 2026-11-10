package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCrashRecoveryMatrix(t *testing.T) {
	packages := t.TempDir()
	priorArchive := makePackage(t, packages, "32.0.0", false)
	candidateArchive := makePackage(t, packages, "32.1.0", false)
	for _, point := range []string{"generation-ready", "bootstrap-intent", "bootstrap-applied", "state-intent", "state-applied", "complete"} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			root := filepath.Join(directory, "install")
			entry := filepath.Join(directory, "entry", "headroom"+nativeExtension())
			prior, err := InstallArchive(priorArchive, root, entry, Expectations{})
			if err != nil {
				t.Fatal(err)
			}
			runCrashHelper(t, []string{"HEADROOM_CRASH_ROOT=" + root, "HEADROOM_CRASH_INSTALL_ARCHIVE=" + candidateArchive, "HEADROOM_CRASH_POINT=install-" + point})
			paths, err := filepath.Glob(filepath.Join(root, "transactions", "install-*", InstallJournalName))
			if err != nil || len(paths) != 1 {
				t.Fatalf("install crash journal paths = %v, %v", paths, err)
			}
			journal, err := readInstallJournal(paths[0], root, filepath.Dir(paths[0]))
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := strings.Replace(point, "applied", "intent", 1)
			if string(journal.Phase) != wantPhase {
				t.Fatalf("install crashed at %s, journal = %s", point, journal.Phase)
			}
			if err := RecoverInstall(root); err != nil {
				t.Fatal(err)
			}
			want := prior
			if point == "complete" {
				want = journal.Candidate
			}
			assertCrashRecovered(t, root, want)
			if data, err := os.ReadFile(paths[0]); err == nil {
				var settled InstallJournal
				if err := json.Unmarshal(data, &settled); err != nil || settled.Phase != journalPhaseRolledBack {
					t.Fatalf("install journal remains nonterminal: %s, %v", data, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			before := crashRecoverySnapshot(t, root, filepath.Join(directory, "no-launches"))
			if err := RecoverInstall(root); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, crashRecoverySnapshot(t, root, filepath.Join(directory, "no-launches"))) {
				t.Fatal("second install recovery changed the settled installation")
			}
		})
	}
}
