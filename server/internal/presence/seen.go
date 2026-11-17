package presence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

const (
	seenFileName = "seen.json"
	seenVersion  = 1
	maxFileBytes = 64 << 10
)

var (
	providerRowPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)
	accountRowPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}:[a-z0-9][a-z0-9_-]{0,31}$`)
)

type fileStatus int

const (
	fileMissing fileStatus = iota
	fileLoaded
	fileCorrupt
	fileNewer
	fileUnreadable
)

type seenFile struct {
	Version int               `json:"version"`
	Rows    map[string]string `json:"rows"`
}

// validRowKey accepts a provider type, optionally followed by one account id.
func validRowKey(key string) bool {
	return providerRowPattern.MatchString(key) || accountRowPattern.MatchString(key)
}

func readSeenFile(path string) (fileStatus, map[string]time.Time) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileMissing, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return fileUnreadable, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileUnreadable, nil
	}
	var file seenFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&file); err != nil {
		return fileCorrupt, nil
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileCorrupt, nil
	}
	if file.Version > seenVersion {
		return fileNewer, nil
	}
	if file.Version != seenVersion {
		return fileCorrupt, nil
	}
	rows := make(map[string]time.Time, len(file.Rows))
	for key, value := range file.Rows {
		if !validRowKey(key) {
			return fileCorrupt, nil
		}
		at, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return fileCorrupt, nil
		}
		rows[key] = at.UTC()
	}
	capRows(rows)
	return fileLoaded, rows
}

func writeSeenFile(dir, path string, rows map[string]time.Time) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := seenFile{Version: seenVersion, Rows: make(map[string]string, len(rows))}
	for key, at := range rows {
		file.Rows[key] = at.UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(file)
	if err != nil || len(data) > maxFileBytes {
		return errors.New("provider presence state is too large")
	}
	temp, err := os.CreateTemp(dir, ".seen-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	tempName = ""
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable where the platform supports it. Windows
// cannot sync a directory handle.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	handle, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}
