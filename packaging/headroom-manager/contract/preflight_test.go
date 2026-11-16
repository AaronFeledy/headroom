package contract

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type managedConfigPreflightFixture struct {
	root        string
	installRoot string
	entry       string
	cliPath     string
	current     *exec.Cmd
	service     *exec.Cmd
	stage       StageResult
}

func newManagedConfigPreflightFixture(t *testing.T, installedVersion, candidateVersion string) *managedConfigPreflightFixture {
	t.Helper()
	f := &managedConfigPreflightFixture{root: t.TempDir()}
	f.installRoot = filepath.Join(f.root, "install")
	f.entry = filepath.Join(f.root, "entry", "headroom-gui"+nativeExtension())
	cliEntry := filepath.Join(f.root, "entry", "headroom"+nativeExtension())
	installed := makePackage(t, f.root, installedVersion, false)
	if _, err := InstallArchiveWithCLIEntry(installed, f.installRoot, f.entry, cliEntry, Expectations{}); err != nil {
		t.Fatal(err)
	}
	f.current = startInstalledFixture(t, f.installRoot)
	var err error
	f.cliPath, _, err = ActiveExecutableForRole(f.installRoot, RoleCLI)
	if err != nil {
		t.Fatal(err)
	}
	f.service = startFixturePath(t, f.cliPath)
	token, err := captureProcessToken(f.service.Process.Pid, f.cliPath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = writeManagedService(f.installRoot, ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: f.installRoot,
		PID: f.service.Process.Pid, Executable: f.cliPath, ProcessToken: token, Arguments: []string{"--config", "fixture.yaml"}, Owner: owner,
		Environment: []string{"USAGE_PROVIDER_CODEX_ENABLED=false"}, WorkingDirectory: f.root, Ready: true}); err != nil {
		t.Fatal(err)
	}
	candidate := makePackage(t, f.root, candidateVersion, false)
	f.stage, err = StageArchive(candidate, f.installRoot, Expectations{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *managedConfigPreflightFixture) request() ApplyRequest {
	return ApplyRequest{InstallRoot: f.installRoot, EntryPath: f.entry, StageRecord: stageRecord(f.stage),
		CurrentPID: f.current.Process.Pid, CurrentExecutable: f.current.Path, WaitTimeoutMS: 3000, ReadyTimeoutMS: 3000}
}

func (f *managedConfigPreflightFixture) requireServiceRunning(t *testing.T) {
	t.Helper()
	if _, err := captureProcessToken(f.service.Process.Pid, f.cliPath); err != nil {
		t.Fatalf("managed service was stopped: %v", err)
	}
	if record, err := ReadManagedService(f.installRoot); err != nil || record.PID != f.service.Process.Pid {
		t.Fatalf("managed service receipt changed: %+v, %v", record, err)
	}
}

func (f *managedConfigPreflightFixture) requireNoApplyTransaction(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.installRoot, "transactions"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "apply-") {
			t.Fatalf("preflight left an apply transaction behind: %s", entry.Name())
		}
	}
}

func TestPrepareApplyRejectsManagedConfigBeforeStoppingService(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	t.Setenv("HEADROOM_FIXTURE_CHECK_CONFIG_FAIL_VERSION", "13.1.0")
	f := newManagedConfigPreflightFixture(t, "13.0.0", "13.1.0")
	before, err := digestFile(filepath.Join(f.installRoot, StateName))
	if err != nil {
		t.Fatal(err)
	}

	_, err = PrepareApply(fixtureExecutable, f.request())

	if err == nil || !strings.Contains(err.Error(), "Headroom 13.1.0 rejects the current server configuration") ||
		!strings.Contains(err.Error(), "config_dir is required") {
		t.Fatalf("preflight error = %v", err)
	}
	f.requireServiceRunning(t)
	after, err := digestFile(filepath.Join(f.installRoot, StateName))
	if err != nil || after != before {
		t.Fatalf("installed state changed during preflight: %s != %s, %v", after, before, err)
	}
	f.requireNoApplyTransaction(t)
}

func TestPrepareApplyRunsManagedConfigPreflightAndProceeds(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	f := newManagedConfigPreflightFixture(t, "13.2.0", "13.3.0")

	prepared, err := PrepareApply(fixtureExecutable, f.request())

	if err != nil {
		t.Fatal(err)
	}
	request, _, err := readApplyRequest(prepared.RequestPath)
	if err != nil {
		t.Fatal(err)
	}
	if request.ManagedServiceWorkingDirectory != f.root || len(request.ManagedServiceArguments) != 2 ||
		request.ManagedServiceArguments[0] != "--config" || request.ManagedServiceArguments[1] != "fixture.yaml" ||
		len(request.ManagedServiceEnvironment) != 1 || request.ManagedServiceEnvironment[0] != "USAGE_PROVIDER_CODEX_ENABLED=false" {
		t.Fatalf("captured managed service request = %+v", request)
	}
	f.requireServiceRunning(t)
}

func TestPrepareApplySkipsManagedConfigPreflightForOlderCandidates(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	t.Setenv("HEADROOM_FIXTURE_CHECK_CONFIG_FAIL_VERSION", "3.5.0")
	f := newManagedConfigPreflightFixture(t, "3.4.0", "3.5.0")

	prepared, err := PrepareApply(fixtureExecutable, f.request())

	if err != nil {
		t.Fatalf("candidate older than 3.6.0 should skip the preflight: %v", err)
	}
	if _, err = os.Stat(prepared.RequestPath); err != nil {
		t.Fatal(err)
	}
	f.requireServiceRunning(t)
}
