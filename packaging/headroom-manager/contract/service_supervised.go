package contract

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
)

const managedSystemdUser = "systemd-user"
const supervisedRestartRecordName = "supervised-restart.json"

type supervisedRestartRecord struct {
	Schema           int      `json:"schema"`
	Product          string   `json:"product"`
	InstallRoot      string   `json:"install_root"`
	Executable       string   `json:"executable"`
	Owner            string   `json:"owner"`
	Arguments        []string `json:"arguments"`
	Environment      []string `json:"environment,omitempty"`
	WorkingDirectory string   `json:"working_directory"`
}

func authorizeSupervisedManagedServiceRestart(root, executable string, arguments, environment []string, workingDirectory string) error {
	owner, err := currentOwnerIdentity()
	if err != nil {
		return err
	}
	record := supervisedRestartRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, Executable: executable, Owner: owner,
		Arguments: append([]string(nil), arguments...), Environment: append([]string(nil), environment...), WorkingDirectory: workingDirectory}
	return WritePrivateJSON(root, filepath.Join("runtime", supervisedRestartRecordName), record)
}

func clearSupervisedManagedServiceRestart(root string) error {
	err := os.Remove(filepath.Join(root, "runtime", supervisedRestartRecordName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func AcceptSupervisedManagedServiceRestart(installRoot string, arguments []string) (ManagedServiceRecord, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	var intent supervisedRestartRecord
	if err = ReadPrivateJSON(root, filepath.Join("runtime", supervisedRestartRecordName), &intent); err != nil {
		return ManagedServiceRecord{}, err
	}
	executable, _ := os.Executable()
	executable, err = filepath.Abs(executable)
	owner, ownerErr := currentOwnerIdentity()
	working, workingErr := os.Getwd()
	if err != nil || ownerErr != nil || workingErr != nil || intent.Schema != SchemaVersion || intent.Product != "Headroom" || intent.InstallRoot != root ||
		intent.Owner != owner || !samePath(intent.Executable, executable) || !slices.Equal(intent.Arguments, arguments) ||
		!slices.Equal(intent.Environment, managedServiceEnvironment(os.Environ())) || intent.WorkingDirectory != working {
		return ManagedServiceRecord{}, errors.New("supervised managed service restart identity is invalid")
	}
	if err = validateManagedServiceSupervisor(managedSystemdUser, os.Getpid()); err != nil {
		return ManagedServiceRecord{}, err
	}
	token, err := captureProcessToken(os.Getpid(), executable)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	record := ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: os.Getpid(), Executable: executable, ProcessToken: token,
		Arguments: append([]string(nil), arguments...), Owner: owner, Supervisor: managedSystemdUser,
		Environment: append([]string(nil), intent.Environment...), WorkingDirectory: working}
	if err = writeManagedService(root, record); err != nil {
		return ManagedServiceRecord{}, err
	}
	return record, nil
}

// AuthorizeSupervisedLauncherHandoff permits only the fixed per-user service's
// public CLI launcher to reach the already-verified candidate while the update
// manager holds the recovery lock. It grants no manager or recovery operation.
func AuthorizeSupervisedLauncherHandoff(installRoot, launcher string, pid int, arguments []string) error {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return err
	}
	if len(arguments) == 0 || arguments[0] != "serve" || validateServiceArguments(arguments[1:]) != nil {
		return errors.New("supervised launcher handoff arguments are invalid")
	}
	launcher, err = normalizeAbsolutePath(launcher, "supervised service launcher")
	if err != nil || pid <= 0 {
		return errors.New("supervised launcher handoff identity is invalid")
	}
	expectedLauncher, _, launcherErr := ActiveExecutableForRole(root, RolePublicLauncher)
	if launcherErr != nil || !samePath(expectedLauncher, launcher) || validateManagedServiceSupervisor(managedSystemdUser, pid) != nil {
		return errors.New("supervised launcher is not the fixed Headroom user service")
	}
	var intent supervisedRestartRecord
	if err = ReadPrivateJSON(root, filepath.Join("runtime", supervisedRestartRecordName), &intent); err != nil {
		return err
	}
	owner, ownerErr := currentOwnerIdentity()
	expectedCLI, inspection, cliErr := ActiveExecutableForRole(root, RoleCLI)
	if ownerErr != nil || cliErr != nil || !inspection.TrustedIdentity || !inspection.Complete || intent.Schema != SchemaVersion || intent.Product != "Headroom" ||
		intent.InstallRoot != root || intent.Owner != owner || !samePath(intent.Executable, expectedCLI) || !slices.Equal(intent.Arguments, arguments[1:]) {
		return errors.New("supervised launcher restart intent is invalid")
	}
	return nil
}
