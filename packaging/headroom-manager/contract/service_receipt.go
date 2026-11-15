package contract

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func clearManagedServiceReceipt(root string) error {
	err := os.Remove(filepath.Join(root, "runtime", ManagedServiceRecordName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
