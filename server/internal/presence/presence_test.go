package presence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func Test_Store_persists_and_reloads_rows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	store := New(Options{Dir: dir})
	if store.Present("Codex") {
		t.Fatal("fresh store remembered a row")
	}

	store.Record("Codex")

	if !store.Present("Codex") {
		t.Fatal("recorded row is not present")
	}
	reloaded := New(Options{Dir: dir})
	if !reloaded.Present("Codex") {
		t.Fatal("reloaded store lost the recorded row")
	}
	rows := readRows(t, dir)
	if rows["Codex"] == "" {
		t.Fatalf("rows = %v, want Codex first-seen time", rows)
	}
	if runtime.GOOS != "windows" {
		fileInfo, err := os.Stat(filepath.Join(dir, seenFileName))
		if err != nil {
			t.Fatalf("stat presence file: %v", err)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("file mode = %o, want 600", got)
		}
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat presence directory: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory mode = %o, want 700", got)
		}
	}
}

func Test_Store_unions_rows_written_by_another_process(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	store := New(Options{Dir: dir})
	writeRows(t, dir, map[string]string{"Grok": "2026-01-01T00:00:00Z"})

	store.Record("Codex")

	rows := readRows(t, dir)
	if rows["Codex"] == "" || rows["Grok"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("rows = %v, want both the new and the concurrently written row", rows)
	}
	if !store.Present("Grok") {
		t.Fatal("store did not adopt the row written by another process")
	}
}

func Test_Store_replaces_a_damaged_file(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"not json", "{"},
		{"unknown version", `{"version":0,"rows":{"Grok":"2026-01-01T00:00:00Z"}}`},
		{"invalid key", `{"version":1,"rows":{"bad key!":"2026-01-01T00:00:00Z"}}`},
		{"invalid time", `{"version":1,"rows":{"Grok":"yesterday"}}`},
		{"trailing data", `{"version":1,"rows":{}} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "provider-presence")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, seenFileName), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			logger, logs := newTestLogger()
			store := New(Options{Dir: dir, Logger: logger})
			if store.Present("Grok") {
				t.Fatal("damaged file contributed a remembered row")
			}

			store.Record("Codex")

			rows := readRows(t, dir)
			if len(rows) != 1 || rows["Codex"] == "" {
				t.Fatalf("rows = %v, want the damaged file replaced with only Codex", rows)
			}
			if got := strings.Count(logs.String(), "level=WARN"); got != 1 {
				t.Fatalf("warnings = %d, want one\n%s", got, logs.String())
			}
		})
	}
}

func Test_Store_leaves_a_newer_version_untouched_and_runs_in_memory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":2,"rows":{"Grok":"2026-01-01T00:00:00Z"}}`)
	path := filepath.Join(dir, seenFileName)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	logger, logs := newTestLogger()
	store := New(Options{Dir: dir, Logger: logger})
	if store.Present("Grok") {
		t.Fatal("newer-schema rows were adopted")
	}

	store.Record("Codex")

	if !store.Present("Codex") {
		t.Fatal("memory-only row is missing")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read presence file: %v", err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Fatalf("newer file was rewritten: %s", onDisk)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("newer version warned: %s", logs.String())
	}
}

func Test_Store_runs_in_memory_when_the_directory_cannot_be_written(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, logs := newTestLogger()
	store := New(Options{Dir: filepath.Join(blocker, "provider-presence"), Logger: logger})

	store.Record("Codex")
	store.Record("Codex")

	if !store.Present("Codex") {
		t.Fatal("memory-only row is missing")
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 1 {
		t.Fatalf("warnings = %d, want one\n%s", got, logs.String())
	}
}

func Test_Store_caps_remembered_rows_at_the_oldest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	store := New(Options{Dir: dir})
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		current = current.Add(time.Second)
		return current
	}
	for index := 0; index < maxRows+4; index++ {
		store.Record(fmt.Sprintf("P%03d", index))
	}

	if store.Present("P000") {
		t.Fatal("oldest row survived the cap")
	}
	if !store.Present(fmt.Sprintf("P%03d", maxRows+3)) {
		t.Fatal("newest row was dropped")
	}
	if got := len(store.rows); got != maxRows {
		t.Fatalf("memory rows = %d, want %d", got, maxRows)
	}
	if got := len(readRows(t, dir)); got != maxRows {
		t.Fatalf("file rows = %d, want %d", got, maxRows)
	}
}

func Test_Store_without_a_directory_works_in_memory_only(t *testing.T) {
	store := New(Options{})
	store.Record("Grok")
	if !store.Present("Grok") {
		t.Fatal("memory-only store lost a row")
	}
	relative := New(Options{Dir: "relative-presence"})
	relative.Record("Grok")
	if relative.dir != "" {
		t.Fatalf("relative directory was used: %q", relative.dir)
	}
}

func Test_Store_ignores_keys_outside_the_wire_format(t *testing.T) {
	store := New(Options{})
	for _, key := range []string{"", "bad key", "Claude:Work", "1Claude", strings.Repeat("A", 33)} {
		store.Record(key)
		if store.Present(key) {
			t.Fatalf("invalid key %q was remembered", key)
		}
	}
	store.Record("Claude:work-1")
	if !store.Present("Claude:work-1") {
		t.Fatal("valid account key was ignored")
	}
}

func Test_Store_caps_rows_read_from_an_oversized_file(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]string, maxRows+4)
	for index := 0; index < maxRows+4; index++ {
		rows[fmt.Sprintf("P%03d", index)] = time.Date(2026, 1, 1, 0, 0, index, 0, time.UTC).Format(time.RFC3339)
	}
	writeRows(t, dir, rows)

	store := New(Options{Dir: dir})
	if got := len(store.rows); got != maxRows {
		t.Fatalf("rows = %d, want %d", got, maxRows)
	}
	if !store.Present(fmt.Sprintf("P%03d", maxRows+3)) {
		t.Fatal("newest row was dropped")
	}
}

func readRows(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, seenFileName))
	if err != nil {
		t.Fatalf("read presence file: %v", err)
	}
	var file seenFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode presence file %s: %v", data, err)
	}
	if file.Version != seenVersion {
		t.Fatalf("version = %d, want %d", file.Version, seenVersion)
	}
	return file.Rows
}

func writeRows(t *testing.T, dir string, rows map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(seenFile{Version: seenVersion, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, seenFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	return slog.New(slog.NewTextHandler(&buffer, nil)), &buffer
}
