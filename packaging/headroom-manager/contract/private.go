package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const maxPrivateJSONBytes = 64 << 10

func WritePrivateJSON(installRoot, relativeName string, value any) error {
	root, path, err := privateJSONPath(installRoot, relativeName)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err = os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if parent == root {
		return errors.New("private JSON must use a private subdirectory")
	}
	if err = securePrivateDirectory(parent, true); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > maxPrivateJSONBytes {
		return errors.New("private JSON is invalid or oversized")
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(parent, ".headroom-private-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	failed := true
	defer func() {
		_ = temporary.Close()
		if failed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = securePrivateFile(temporaryName, true); err != nil {
		return err
	}
	if err = replaceAtomic(temporaryName, path); err != nil {
		return err
	}
	failed = false
	return syncDirectory(parent)
}

func ReadPrivateJSON(installRoot, relativeName string, value any) error {
	return readPrivateJSON(installRoot, relativeName, value, true)
}

// readPrivateJSONRelaxed decodes a private file a later release may extend, so
// an earlier release can still read it. Only unknown fields are tolerated:
// duplicate keys, trailing data, ownership, permissions, and size limits are
// enforced exactly as for every other private file.
func readPrivateJSONRelaxed(installRoot, relativeName string, value any) error {
	return readPrivateJSON(installRoot, relativeName, value, false)
}

func readPrivateJSON(installRoot, relativeName string, value any, strictFields bool) error {
	root, path, err := privateJSONPath(installRoot, relativeName)
	if err != nil {
		return err
	}
	if filepath.Dir(path) == root {
		return errors.New("private JSON must use a private subdirectory")
	}
	if err = securePrivateDirectory(filepath.Dir(path), false); err != nil {
		return err
	}
	if err = securePrivateFile(path, false); err != nil {
		return err
	}
	data, err := readBoundedFile(path, maxPrivateJSONBytes)
	if err != nil {
		return err
	}
	if err = validatePrivateUniqueJSON(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if strictFields {
		decoder.DisallowUnknownFields()
	}
	if decoder.Decode(value) != nil || ensureEOF(decoder) != nil {
		return errors.New("private JSON is invalid")
	}
	return nil
}

func validatePrivateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("private JSON is too deeply nested")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, keyOK := keyToken.(string)
				if keyErr != nil || !keyOK || seen[key] {
					return errors.New("private JSON has duplicate or invalid keys")
				}
				seen[key] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim('}') {
				return errors.New("private JSON object is invalid")
			}
			return nil
		}
		if delim == '[' {
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim(']') {
				return errors.New("private JSON array is invalid")
			}
			return nil
		}
		return errors.New("private JSON delimiter is invalid")
	}
	if err := walk(0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("private JSON has trailing data")
	}
	return nil
}

func privateJSONPath(installRoot, relativeName string) (string, string, error) {
	root, err := NormalizeInstallRoot(installRoot)
	if err != nil {
		return "", "", err
	}
	if err = validateInstallTargets(root, ""); err != nil {
		return "", "", err
	}
	if relativeName == "" || strings.ContainsAny(relativeName, "\r\n\x00") || filepath.IsAbs(relativeName) || filepath.VolumeName(relativeName) != "" {
		return "", "", errors.New("private JSON path is invalid")
	}
	portableName := filepath.ToSlash(relativeName)
	if runtime.GOOS != "windows" && strings.Contains(relativeName, "\\") || path.Clean(portableName) != portableName || strings.HasPrefix(portableName, "../") {
		return "", "", errors.New("private JSON path is invalid")
	}
	localPath := filepath.Join(root, filepath.FromSlash(portableName))
	if filepath.Dir(localPath) == root || filepath.Dir(filepath.Dir(localPath)) == root {
		return root, localPath, nil
	}
	return "", "", fmt.Errorf("private JSON path is too deep")
}
