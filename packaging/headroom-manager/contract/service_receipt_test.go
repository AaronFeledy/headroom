package contract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func testManagedServiceRecord(t *testing.T, root string) ManagedServiceRecord {
	t.Helper()
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: 4321,
		Executable: filepath.Join(root, "bin", "headroom"), ProcessToken: "kernel-process-token",
		Arguments: []string{"--config", "fixture.yaml"}, Owner: owner,
		Environment: []string{"USAGE_PROVIDER_CODEX_ENABLED=false"}, WorkingDirectory: root}
}

func managedServiceReceiptPaths(root string) (string, string) {
	return filepath.Join(root, "runtime", ManagedServiceRecordName), filepath.Join(root, "runtime", managedServiceConfigRecordName)
}

// olderManagedServiceRecord is the exact receipt shape released in 3.5.0. An
// updater that shipped with it decodes runtime/managed-serve.json with unknown
// fields disallowed.
type olderManagedServiceRecord struct {
	Schema             int      `json:"schema"`
	Product            string   `json:"product"`
	InstallRoot        string   `json:"install_root"`
	PID                int      `json:"pid"`
	Executable         string   `json:"executable"`
	ProcessToken       string   `json:"process_token"`
	Arguments          []string `json:"arguments"`
	LaunchToken        string   `json:"launch_token,omitempty"`
	Owner              string   `json:"owner"`
	Environment        []string `json:"environment,omitempty"`
	WorkingDirectory   string   `json:"working_directory,omitempty"`
	LauncherPID        int      `json:"launcher_pid,omitempty"`
	LauncherExecutable string   `json:"launcher_executable,omitempty"`
	LauncherToken      string   `json:"launcher_process_token,omitempty"`
	Supervisor         string   `json:"supervisor,omitempty"`
	Ready              bool     `json:"ready"`
}

func TestManagedServiceReceiptKeepsOlderUpdaterFieldSet(t *testing.T) {
	root := t.TempDir()
	record := testManagedServiceRecord(t, root)
	record.Ready = true
	record.LaunchToken = strings.Repeat("a", 48)
	record.Supervisor = managedSystemdUser
	record.LauncherPID = 99
	record.LauncherExecutable = filepath.Join(root, "launcher")
	record.LauncherToken = "launcher-process-token"
	record.ConfigPath = filepath.Join(root, "config", "headroom.yaml")
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	receiptPath, _ := managedServiceReceiptPaths(root)
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sort.Strings(got)
	// Older updaters decode runtime/managed-serve.json with
	// DisallowUnknownFields, so adding a field here breaks every update from
	// them. Keep the exact 3.5.0 set; new state belongs in a sidecar.
	want := []string{"arguments", "environment", "executable", "install_root", "launch_token",
		"launcher_executable", "launcher_pid", "launcher_process_token", "owner", "pid", "process_token",
		"product", "ready", "schema", "supervisor", "working_directory"}
	if !slices.Equal(got, want) {
		t.Fatalf("receipt fields = %v, want the 3.5.0 set %v", got, want)
	}
	var older olderManagedServiceRecord
	if err := ReadPrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName), &older); err != nil {
		t.Fatalf("3.5.0 receipt reader rejected this receipt: %v", err)
	}
	if !older.Ready || older.PID != record.PID || older.Schema != SchemaVersion {
		t.Fatalf("3.5.0 receipt reader decoded %+v", older)
	}
}

func TestManagedServiceConfigSidecarBindsToReceiptIdentity(t *testing.T) {
	root := t.TempDir()
	record := testManagedServiceRecord(t, root)
	record.ConfigPath = filepath.Join(root, "config", "headroom.yaml")
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	stored, err := readManagedService(root)
	if err != nil || stored.ConfigPath != record.ConfigPath {
		t.Fatalf("bound config path = %q, %v", stored.ConfigPath, err)
	}
	current := func() managedServiceConfigRecord {
		return managedServiceConfigRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: record.PID,
			ProcessToken: record.ProcessToken, Executable: record.Executable, Owner: record.Owner, ConfigPath: record.ConfigPath}
	}
	store := func(value managedServiceConfigRecord) {
		t.Helper()
		if err := WritePrivateJSON(root, filepath.Join("runtime", managedServiceConfigRecordName), value); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(*managedServiceConfigRecord){
		"pid":           func(value *managedServiceConfigRecord) { value.PID++ },
		"process_token": func(value *managedServiceConfigRecord) { value.ProcessToken = "replaced-process" },
		"executable":    func(value *managedServiceConfigRecord) { value.Executable = filepath.Join(root, "bin", "other") },
		"install_root":  func(value *managedServiceConfigRecord) { value.InstallRoot = filepath.Join(root, "other") },
		"owner":         func(value *managedServiceConfigRecord) { value.Owner = "other-owner" },
	} {
		t.Run("stale_"+name, func(t *testing.T) {
			value := current()
			mutate(&value)
			store(value)
			stored, err := readManagedService(root)
			if err != nil || stored.ConfigPath != "" {
				t.Fatalf("stale sidecar (%s) config path = %q, %v", name, stored.ConfigPath, err)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		_, sidecarPath := managedServiceReceiptPaths(root)
		if err := os.Remove(sidecarPath); err != nil {
			t.Fatal(err)
		}
		stored, err := readManagedService(root)
		if err != nil || stored.ConfigPath != "" {
			t.Fatalf("missing sidecar config path = %q, %v", stored.ConfigPath, err)
		}
	})
}

func TestManagedServiceReceiptUsesLegacyInlineConfigPath(t *testing.T) {
	root := t.TempDir()
	record := testManagedServiceRecord(t, root)
	legacy := filepath.Join(root, "legacy.yaml")
	// 3.6.0 wrote the resolved path inline in the receipt.
	if err := WritePrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName),
		managedServiceReceipt{ManagedServiceRecord: record, LegacyConfigPath: legacy}); err != nil {
		t.Fatal(err)
	}
	stored, err := readManagedService(root)
	if err != nil || stored.ConfigPath != legacy {
		t.Fatalf("legacy config path = %q, %v", stored.ConfigPath, err)
	}
	// A sidecar bound to the same process wins over the inline field.
	record.ConfigPath = filepath.Join(root, "sidecar.yaml")
	if err := writeManagedServiceConfig(root, record); err != nil {
		t.Fatal(err)
	}
	stored, err = readManagedService(root)
	if err != nil || stored.ConfigPath != record.ConfigPath {
		t.Fatalf("sidecar config path = %q, %v", stored.ConfigPath, err)
	}
}

func TestManagedServiceReceiptToleratesFutureFields(t *testing.T) {
	root := t.TempDir()
	record := testManagedServiceRecord(t, root)
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_field"] = map[string]any{"added": "later"}
	if err := WritePrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName), fields); err != nil {
		t.Fatal(err)
	}
	stored, err := readManagedService(root)
	if err != nil || stored.PID != record.PID || stored.ConfigPath != "" {
		t.Fatalf("future receipt fields = %+v, %v", stored, err)
	}
	// Only the receipt tolerates unknown fields; every other private file is
	// still decoded strictly.
	if err := WritePrivateJSON(root, filepath.Join("runtime", "fixture.json"), map[string]any{"name": "value", "future_field": true}); err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Name string `json:"name"`
	}
	if err := ReadPrivateJSON(root, filepath.Join("runtime", "fixture.json"), &fixture); err == nil {
		t.Fatal("strict private JSON accepted an unknown field")
	}
}

func TestClearingManagedServiceReceiptRemovesConfigSidecar(t *testing.T) {
	root := t.TempDir()
	record := testManagedServiceRecord(t, root)
	record.ConfigPath = filepath.Join(root, "config", "headroom.yaml")
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	receiptPath, sidecarPath := managedServiceReceiptPaths(root)
	for _, path := range []string{receiptPath, sidecarPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was not written: %v", path, err)
		}
	}
	if err := clearManagedServiceReceipt(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{receiptPath, sidecarPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains after clearing: %v", path, err)
		}
	}
	// A receipt written without a config path drops a leftover sidecar too.
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	record.ConfigPath = ""
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecarPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar remains after a receipt without a config path: %v", err)
	}
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("receipt missing: %v", err)
	}
}

func TestUnregisterManagedServiceRemovesConfigSidecar(t *testing.T) {
	t.Setenv("HEADROOM_FIXTURE_SLEEP_MS", "60000")
	root := t.TempDir()
	service := startFixturePath(t, fixtureExecutable)
	token, err := captureProcessToken(service.Process.Pid, fixtureExecutable)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	record := ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: service.Process.Pid,
		Executable: fixtureExecutable, ProcessToken: token, Owner: owner, WorkingDirectory: root,
		ConfigPath: filepath.Join(root, "config", "headroom.yaml")}
	if err := writeManagedService(root, record); err != nil {
		t.Fatal(err)
	}
	if err := UnregisterManagedService(record); err != nil {
		t.Fatal(err)
	}
	receiptPath, sidecarPath := managedServiceReceiptPaths(root)
	for _, path := range []string{receiptPath, sidecarPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains after unregistering: %v", path, err)
		}
	}
}
