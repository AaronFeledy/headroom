package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func startCrashManagedService(t *testing.T, root string) {
	t.Helper()
	executable, _, err := ActiveExecutableForRole(root, RoleCLI)
	if err != nil {
		t.Fatal(err)
	}
	service := startFixturePath(t, executable)
	token, err := captureProcessToken(service.Process.Pid, executable)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(root)
	if err := writeManagedService(root, ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root,
		PID: service.Process.Pid, Executable: executable, ProcessToken: token, Owner: owner, Ready: true,
		Arguments:   []string{"--headroom-fixture-log", filepath.Join(parent, "service-launches.jsonl"), "--headroom-fixture-sleep", "60000"},
		Environment: []string{"USAGE_CONFIG=" + filepath.Join(parent, "service-evidence")}, WorkingDirectory: parent}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(crashLaunch{PID: service.Process.Pid, Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "original-service.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
