package contract

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const managedServiceConfigRecordName = "managed-serve-config.json"

// managedServiceConfigRecord binds the resolved config path to the exact
// process reported ready. It lives beside the receipt because older updaters
// decode the receipt itself with unknown fields disallowed.
type managedServiceConfigRecord struct {
	Schema       int    `json:"schema"`
	Product      string `json:"product"`
	InstallRoot  string `json:"install_root"`
	PID          int    `json:"pid"`
	ProcessToken string `json:"process_token"`
	Executable   string `json:"executable"`
	Owner        string `json:"owner"`
	ConfigPath   string `json:"config_path"`
}

// managedServiceReceipt also accepts the config_path that 3.6.0 wrote inline.
// This version never serializes it there; see writeManagedServiceConfig.
type managedServiceReceipt struct {
	ManagedServiceRecord
	LegacyConfigPath string `json:"config_path,omitempty"`
}

func clearManagedServiceReceipt(root string) error {
	return errors.Join(removePrivateFile(filepath.Join(root, "runtime", ManagedServiceRecordName)),
		clearManagedServiceConfig(root))
}

func removePrivateFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func clearManagedServiceConfig(root string) error {
	return removePrivateFile(filepath.Join(root, "runtime", managedServiceConfigRecordName))
}

func readManagedService(root string) (ManagedServiceRecord, error) {
	var receipt managedServiceReceipt
	var err error
	if err := readPrivateJSONRelaxed(root, filepath.Join("runtime", ManagedServiceRecordName), &receipt); err != nil {
		return ManagedServiceRecord{}, err
	}
	record := receipt.ManagedServiceRecord
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
	record.ConfigPath = managedServiceConfigPath(root, record, receipt.LegacyConfigPath)
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
	if err := writeManagedServiceConfig(root, record); err != nil {
		return err
	}
	return WritePrivateJSON(root, filepath.Join("runtime", ManagedServiceRecordName), record)
}

// writeManagedServiceConfig records the resolved config path beside the
// receipt, bound to the same process identity. It runs before the receipt it
// belongs to becomes visible, so a ready receipt never lacks its sidecar.
// Written without a config path, the receipt drops any previous sidecar.
func writeManagedServiceConfig(root string, record ManagedServiceRecord) error {
	if record.ConfigPath == "" {
		return clearManagedServiceConfig(root)
	}
	normalizedRoot, err := NormalizeInstallRoot(root)
	if err != nil {
		return err
	}
	configPath, err := normalizeAbsolutePath(record.ConfigPath, "managed service config file")
	if err != nil {
		return err
	}
	return WritePrivateJSON(root, filepath.Join("runtime", managedServiceConfigRecordName), managedServiceConfigRecord{
		Schema: SchemaVersion, Product: "Headroom", InstallRoot: normalizedRoot, PID: record.PID,
		ProcessToken: record.ProcessToken, Executable: record.Executable, Owner: record.Owner, ConfigPath: configPath})
}

// managedServiceConfigPath returns the resolved config path only when its
// record belongs to this receipt's process identity; a stale or mismatched
// sidecar is ignored. A 3.6.0 receipt carried the path inline instead, and
// that legacy value is used when no matching sidecar exists.
func managedServiceConfigPath(root string, record ManagedServiceRecord, legacy string) string {
	var stored managedServiceConfigRecord
	if err := readPrivateJSONRelaxed(root, filepath.Join("runtime", managedServiceConfigRecordName), &stored); err == nil &&
		stored.Schema == SchemaVersion && stored.Product == "Headroom" && stored.InstallRoot == root &&
		stored.PID == record.PID && stored.ProcessToken == record.ProcessToken && stored.Owner == record.Owner &&
		samePath(stored.Executable, record.Executable) {
		if path, pathErr := normalizeAbsolutePath(stored.ConfigPath, "managed service config file"); pathErr == nil {
			return path
		}
	}
	if path, err := normalizeAbsolutePath(legacy, "managed service config file"); err == nil {
		return path
	}
	return ""
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
