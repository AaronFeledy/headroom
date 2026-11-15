package contract

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"
)

var errProcessExecutableMismatch = errors.New("process executable identity does not match")

// A failed identity query is not evidence that a live service was replaced.
// Only a different kernel identity or a definitely dead PID permits replacing
// its receipt. A PID reused by another user's process after a reboot hides its
// executable but not its creation identity. The caller holds the install
// transaction lock.
func managedServiceMayRegister(record ManagedServiceRecord) error {
	token, err := captureProcessToken(record.PID, record.Executable)
	if err == nil {
		if token != record.ProcessToken {
			return nil
		}
		return errors.New("a managed Headroom service is already running")
	}
	if errors.Is(err, errProcessExecutableMismatch) || processGone(record.PID) || processCreationChanged(record.PID, record.ProcessToken) {
		return nil
	}
	return errors.New("cannot verify the existing managed Headroom service; its receipt was preserved")
}

const ManagedServiceRecordName = "managed-serve.json"

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
	ConfigPath         string   `json:"config_path,omitempty"`
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
	current.ConfigPath = record.ConfigPath
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
