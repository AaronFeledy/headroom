package contract

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryCrashHelper(t *testing.T) {
	point := os.Getenv("HEADROOM_CRASH_POINT")
	if point == "" {
		return
	}
	root := os.Getenv("HEADROOM_CRASH_ROOT")
	transactionTestHook = func(phase string) error {
		if phase == point {
			os.Exit(86)
		}
		if phase == "candidate-launched" && (point == "rolling-back" || point == "rollback-relaunch" || point == "rolled-back" || point == "rollback-generation-removed") {
			return errors.New("matrix candidate failure")
		}
		if phase == "generation-ready" && point == "rollback-generation-only-removed" {
			return errors.New("matrix generation-ready failure")
		}
		return nil
	}
	if archive := os.Getenv("HEADROOM_CRASH_INSTALL_ARCHIVE"); archive != "" {
		if _, err := InstallArchive(archive, root, filepath.Join(filepath.Dir(root), "entry", "headroom"+nativeExtension()), Expectations{}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("install did not reach crash point")
	}
	if os.Getenv("HEADROOM_CRASH_RECOVER") != "" {
		if err := RecoverInstall(root); err != nil {
			t.Fatal(err)
		}
		t.Fatal("recovery did not reach crash point")
	}
	current := startInstalledFixture(t, root)
	prepared, err := PrepareApply(fixtureExecutable, ApplyRequest{
		InstallRoot: root, EntryPath: filepath.Join(filepath.Dir(root), "entry", "headroom"+nativeExtension()),
		StageRecord: os.Getenv("HEADROOM_CRASH_STAGE"), CurrentPID: current.Process.Pid, CurrentExecutable: current.Path,
		RelaunchArguments: []string{"--headroom-fixture-log", filepath.Join(filepath.Dir(root), "launches.jsonl"), "--headroom-fixture-sleep", "60000"},
		WaitTimeoutMS:     3000, ReadyTimeoutMS: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := WaitForApplyAcknowledgement(prepared, 10*time.Second); err != nil {
			panic(err)
		}
		authorizePreparedApply(t, prepared)
		if err := current.Process.Kill(); err != nil {
			panic(err)
		}
		_, _ = current.Process.Wait()
	}()
	if _, err = ApplyPrepared(prepared.RequestPath); err != nil {
		t.Fatal(err)
	}
	t.Fatal("apply did not reach crash point")
}
