package contract

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

var errProcessExecutableMismatch = errors.New("process executable identity does not match")

// A failed identity query is not evidence that a live service was replaced.
// Only a different kernel identity or a definitely dead PID permits replacing
// its receipt. The caller holds the install transaction lock.
func managedServiceMayRegister(record ManagedServiceRecord) error {
	token, err := captureProcessToken(record.PID, record.Executable)
	if err == nil {
		if token != record.ProcessToken {
			return nil
		}
		return errors.New("a managed Headroom service is already running")
	}
	if errors.Is(err, errProcessExecutableMismatch) || processGone(record.PID) {
		return nil
	}
	return errors.New("cannot verify the existing managed Headroom service; its receipt was preserved")
}

const ManagedServiceRecordName = "managed-serve.json"
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

func clearManagedServiceReceipt(root string) error {
	err := os.Remove(filepath.Join(root, "runtime", ManagedServiceRecordName))
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

type ManagedServiceRecord struct {
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

func RegisterManagedService(installRoot string, arguments []string) (ManagedServiceRecord, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	if err = validateServiceArguments(arguments); err != nil {
		return ManagedServiceRecord{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	expected, _, err := ActiveExecutableForRole(root, RoleCLI)
	if err != nil || !samePath(expected, executable) {
		return ManagedServiceRecord{}, errors.New("managed serve requires the active verified Headroom CLI")
	}
	token, err := captureProcessToken(os.Getpid(), executable)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	lock, err := acquireInstallLock(root, 10*time.Second)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	defer lock.Close()
	if existing, readErr := readManagedService(root); readErr == nil {
		if err := managedServiceMayRegister(existing); err != nil {
			return ManagedServiceRecord{}, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return ManagedServiceRecord{}, readErr
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	workingDirectory, err = normalizeAbsolutePath(workingDirectory, "managed service working directory")
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	owner, err := currentOwnerIdentity()
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	record := ManagedServiceRecord{Schema: SchemaVersion, Product: "Headroom", InstallRoot: root, PID: os.Getpid(), Executable: executable, ProcessToken: token,
		Arguments: append([]string(nil), arguments...), Owner: owner, Environment: managedServiceEnvironment(os.Environ()), WorkingDirectory: workingDirectory}
	record.Supervisor, err = managedServiceSupervisor(os.Environ(), record.PID)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	if runtime.GOOS == "windows" {
		launcherPIDValue, launcherPath := os.Getenv("HEADROOM_PUBLIC_LAUNCHER_PID"), os.Getenv("HEADROOM_PUBLIC_LAUNCHER_PATH")
		if launcherPIDValue != "" || launcherPath != "" {
			launcherPID, parseErr := strconv.Atoi(launcherPIDValue)
			expectedLauncher, _, expectedErr := ActiveExecutableForRole(root, RolePublicLauncher)
			if parseErr != nil || launcherPID <= 0 || !filepath.IsAbs(launcherPath) || expectedErr != nil || !samePath(expectedLauncher, launcherPath) {
				return ManagedServiceRecord{}, errors.New("managed Windows serve has an invalid public launcher identity")
			}
			record.LauncherToken, err = captureProcessToken(launcherPID, launcherPath)
			if err != nil {
				return ManagedServiceRecord{}, err
			}
			record.LauncherPID, record.LauncherExecutable = launcherPID, filepath.Clean(launcherPath)
		}
	}
	if err = writeManagedService(root, record); err != nil {
		return ManagedServiceRecord{}, err
	}
	return record, nil
}

func UnregisterManagedService(record ManagedServiceRecord) error {
	root, err := NormalizeInstallRoot(record.InstallRoot)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(root, 0)
	if err != nil {
		if err.Error() == "Headroom install transaction is busy" {
			// The updater already owns the exact receipt and will replace or
			// remove it. Service shutdown must never wait on that updater.
			return nil
		}
		return err
	}
	defer lock.Close()
	current, err := readManagedService(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.PID != record.PID || current.ProcessToken != record.ProcessToken || !samePath(current.Executable, record.Executable) {
		return errors.New("managed service receipt changed")
	}
	return os.Remove(filepath.Join(root, "runtime", ManagedServiceRecordName))
}

func ReadManagedService(installRoot string) (ManagedServiceRecord, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	record, err := readManagedService(root)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	if !record.Ready {
		return ManagedServiceRecord{}, errors.New("managed service has not reported readiness")
	}
	expected, _, expectedErr := ActiveExecutableForRole(root, RoleCLI)
	if expectedErr != nil || !samePath(expected, record.Executable) {
		return ManagedServiceRecord{}, errors.New("managed service executable is not active")
	}
	token, tokenErr := captureProcessToken(record.PID, record.Executable)
	if tokenErr != nil && processGone(record.PID) {
		// This read path holds no install lock, so it must not delete the
		// receipt: registration may already have replaced it with a live
		// service. Registration overwrites a dead receipt under the lock.
		return ManagedServiceRecord{}, os.ErrNotExist
	}
	if tokenErr != nil || token != record.ProcessToken {
		return ManagedServiceRecord{}, errors.New("managed service process identity is invalid")
	}
	if record.LauncherPID > 0 {
		launcherToken, launcherErr := captureProcessToken(record.LauncherPID, record.LauncherExecutable)
		if launcherErr != nil || launcherToken != record.LauncherToken {
			return ManagedServiceRecord{}, errors.New("managed service launcher identity is invalid")
		}
	}
	if err = validateManagedServiceSupervisor(record.Supervisor, record.PID); err != nil {
		return ManagedServiceRecord{}, err
	}
	return record, nil
}

// MarkManagedServiceReady binds readiness to the exact process receipt. It is
// lock-free because an update manager holds the install lock while authorizing
// a replacement service and waits for this acknowledgement.
func MarkManagedServiceReady(record ManagedServiceRecord) error {
	root, err := NormalizeInstallRoot(record.InstallRoot)
	if err != nil {
		return err
	}
	current, err := readManagedService(root)
	if err != nil {
		return err
	}
	if current.PID != record.PID || current.ProcessToken != record.ProcessToken || !samePath(current.Executable, record.Executable) ||
		current.LaunchToken != "" || current.Supervisor != record.Supervisor || current.Owner != record.Owner ||
		!slices.Equal(current.Arguments, record.Arguments) || !slices.Equal(current.Environment, record.Environment) || current.WorkingDirectory != record.WorkingDirectory {
		return errors.New("managed service readiness identity changed")
	}
	token, tokenErr := captureProcessToken(current.PID, current.Executable)
	expected, _, activeErr := ActiveExecutableForRole(root, RoleCLI)
	if tokenErr != nil || token != current.ProcessToken || activeErr != nil || !samePath(expected, current.Executable) || validateManagedServiceSupervisor(current.Supervisor, current.PID) != nil {
		return errors.New("managed service readiness process is invalid")
	}
	if current.Supervisor != "" {
		if err = clearSupervisedManagedServiceRestart(root); err != nil {
			return err
		}
	}
	current.Ready = true
	return writeManagedService(root, current)
}

func AcceptManagedServiceRestart(installRoot, launchToken string, arguments []string) (ManagedServiceRecord, error) {
	if len(launchToken) != 48 || !isLowerHex(launchToken) || validateServiceArguments(arguments) != nil {
		return ManagedServiceRecord{}, errors.New("managed service restart identity is invalid")
	}
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return ManagedServiceRecord{}, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, readErr := readManagedService(root)
		if readErr == nil && record.LaunchToken == launchToken && record.PID == os.Getpid() && samePath(record.Executable, executable) && slices.Equal(record.Arguments, arguments) {
			token, tokenErr := captureProcessToken(record.PID, record.Executable)
			if tokenErr == nil && token == record.ProcessToken && validateManagedServiceSupervisor(record.Supervisor, record.PID) == nil {
				record.LaunchToken = ""
				if writeErr := writeManagedService(root, record); writeErr != nil {
					return ManagedServiceRecord{}, writeErr
				}
				return record, nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ManagedServiceRecord{}, errors.New("managed service restart was not authorized")
}

func readManagedService(root string) (ManagedServiceRecord, error) {
	var record ManagedServiceRecord
	var err error
	if err := ReadPrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName), &record); err != nil {
		return ManagedServiceRecord{}, err
	}
	if record.Schema != SchemaVersion || record.Product != "Headroom" || record.InstallRoot != root || record.PID <= 0 || record.ProcessToken == "" {
		return ManagedServiceRecord{}, errors.New("managed service receipt is invalid")
	}
	owner, ownerErr := currentOwnerIdentity()
	if ownerErr != nil || record.Owner != owner {
		return ManagedServiceRecord{}, errors.New("managed service owner is invalid")
	}
	record.Executable, err = normalizeAbsolutePath(record.Executable, "managed service executable")
	if err != nil {
		return ManagedServiceRecord{}, errors.New("managed service receipt is invalid")
	}
	record.WorkingDirectory, err = normalizeAbsolutePath(record.WorkingDirectory, "managed service working directory")
	if err != nil || validateServiceArguments(record.Arguments) != nil || validateServiceEnvironment(record.Environment) != nil {
		return ManagedServiceRecord{}, errors.New("managed service receipt is invalid")
	}
	if record.LauncherPID > 0 || record.LauncherExecutable != "" || record.LauncherToken != "" {
		record.LauncherExecutable, err = normalizeAbsolutePath(record.LauncherExecutable, "managed service launcher")
		if err != nil || record.LauncherPID <= 0 || record.LauncherToken == "" {
			return ManagedServiceRecord{}, errors.New("managed service launcher receipt is invalid")
		}
		expected, _, expectedErr := ActiveExecutableForRole(root, RolePublicLauncher)
		if expectedErr != nil || !samePath(expected, record.LauncherExecutable) {
			return ManagedServiceRecord{}, errors.New("managed service launcher is not active")
		}
	}
	return record, nil
}

func managedServiceEnvironment(environment []string) []string {
	result := make([]string, 0)
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		checkedKey := key
		if runtime.GOOS == "windows" {
			checkedKey = strings.ToUpper(key)
		}
		allowed := checkedKey == "USAGE_CONFIG" || checkedKey == "USAGE_LISTEN_ADDR" || checkedKey == "USAGE_AUTH_TOKEN" || checkedKey == "USAGE_POLL_INTERVAL" || checkedKey == "USAGE_SSH_ACCESS" || checkedKey == "USAGE_TLS" || checkedKey == "USAGE_TLS_CERT_FILE" || checkedKey == "USAGE_TLS_KEY_FILE" ||
			(strings.HasPrefix(checkedKey, "USAGE_PROVIDER_") && (strings.HasSuffix(checkedKey, "_ENABLED") || strings.HasSuffix(checkedKey, "_CREDENTIALS_PATH") || strings.HasSuffix(checkedKey, "_BROWSER_CREDENTIALS")))
		if allowed {
			result = append(result, item)
		}
	}
	return result
}

func validateServiceEnvironment(environment []string) error {
	if len(environment) > 64 {
		return errors.New("managed service environment is invalid")
	}
	seen := make(map[string]struct{}, len(environment))
	for _, item := range environment {
		if len(item) > 16384 || strings.ContainsRune(item, 0) {
			return errors.New("managed service environment is invalid")
		}
		key, _, _ := strings.Cut(item, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if _, exists := seen[key]; exists {
			return errors.New("managed service environment is invalid")
		}
		seen[key] = struct{}{}
		filtered := managedServiceEnvironment([]string{item})
		if len(filtered) != 1 || filtered[0] != item {
			return errors.New("managed service environment is invalid")
		}
	}
	return nil
}

func writeManagedService(root string, record ManagedServiceRecord) error {
	return WritePrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName), record)
}

func validateServiceArguments(arguments []string) error {
	if len(arguments) > 32 {
		return errors.New("managed serve has too many arguments")
	}
	for _, argument := range arguments {
		if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return errors.New("managed serve argument is invalid")
		}
	}
	return nil
}
