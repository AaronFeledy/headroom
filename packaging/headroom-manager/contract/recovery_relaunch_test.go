package contract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryReadyRequiresLivePriorRoleAndVersion(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	parent := t.TempDir()
	root := filepath.Join(parent, "install")
	prior, err := InstallArchive(makePackage(t, parent, "32.0.0", false), root, filepath.Join(parent, "entry", "headroom"+nativeExtension()), Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	app := startInstalledFixture(t, root)
	cliPath, _, err := ActiveExecutableForRole(root, RoleCLI)
	if err != nil {
		t.Fatal(err)
	}
	cli := startFixturePath(t, cliPath)
	dead := startInstalledFixture(t, root)
	if err := dead.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	stage, err := StageArchive(makePackage(t, parent, "32.1.0", false), root, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	stage, manifest, err := LoadVerifiedStage(root, stageRecord(stage))
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := createGeneration(root, stage, manifest)
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := installedComponentPath(root, candidate.VersionPath, manifest.Components.Application.Path)
	candidateApp := startFixturePath(t, candidatePath)
	for _, test := range []struct {
		name  string
		role  string
		ready crashLaunch
		want  bool
	}{
		{"live app", RoleApplication, crashLaunch{app.Process.Pid, app.Path, prior.ActiveVersion}, true},
		{"live CLI", RoleCLI, crashLaunch{cli.Process.Pid, cliPath, prior.ActiveVersion}, true},
		{"wrong version", RoleApplication, crashLaunch{app.Process.Pid, app.Path, "32.1.0"}, false},
		{"wrong role", RoleApplication, crashLaunch{cli.Process.Pid, cliPath, prior.ActiveVersion}, false},
		{"other process at prior path", RoleApplication, crashLaunch{cli.Process.Pid, app.Path, prior.ActiveVersion}, false},
		{"candidate path", RoleApplication, crashLaunch{candidateApp.Process.Pid, candidatePath, prior.ActiveVersion}, false},
		{"candidate PID at prior path", RoleApplication, crashLaunch{candidateApp.Process.Pid, app.Path, prior.ActiveVersion}, false},
		{"dead process", RoleApplication, crashLaunch{dead.Process.Pid, dead.Path, prior.ActiveVersion}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"rollback-ready.json", "recovery-ready.json"} {
				directory := t.TempDir()
				if err := writeDurableJSON(filepath.Join(directory, name), test.ready); err != nil {
					t.Fatal(err)
				}
				journal := ApplyJournal{Request: ApplyRequest{InstallRoot: root, CandidateRole: test.role}, Previous: &prior}
				if got := priorAppReady(journal, directory); got != test.want {
					t.Errorf("%s accepted = %v; want %v", name, got, test.want)
				}
			}
		})
	}
	journal := ApplyJournal{Request: ApplyRequest{InstallRoot: root}, Previous: &prior}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "rollback-ready.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if priorAppReady(journal, directory) {
		t.Fatal("malformed marker accepted")
	}
	if err := writeDurableJSON(filepath.Join(directory, "recovery-ready.json"), crashLaunch{app.Process.Pid, app.Path, prior.ActiveVersion}); err != nil {
		t.Fatal(err)
	}
	if !priorAppReady(journal, directory) {
		t.Fatal("invalid rollback marker hid valid recovery marker")
	}
	token, err := captureProcessToken(cli.Process.Pid, cliPath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	journal.Request.AdditionalProcesses = []ProcessClaim{{Role: RoleManagedServer}}
	record := ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: cli.Process.Pid,
		Executable: cliPath, ProcessToken: token, Owner: owner, WorkingDirectory: parent, Ready: true}
	for _, name := range []string{"live prior", "changed creation token", "candidate executable", "dead process", "not ready"} {
		t.Run("service/"+name, func(t *testing.T) {
			modified := record
			switch name {
			case "changed creation token":
				modified.ProcessToken = "different-kernel-start-identity"
			case "candidate executable":
				modified.Executable, modified.PID = candidatePath, candidateApp.Process.Pid
			case "dead process":
				modified.PID = dead.Process.Pid
			case "not ready":
				modified.Ready = false
			}
			if err := writeManagedService(root, modified); err != nil {
				t.Fatal(err)
			}
			if got := priorManagedServiceReady(journal); got != (name == "live prior") {
				t.Errorf("service accepted = %v; want %v", got, name == "live prior")
			}
		})
	}
}
