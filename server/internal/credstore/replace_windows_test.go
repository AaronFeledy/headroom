//go:build windows

package credstore

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func Test_replaceExisting_replaces_a_destination_held_open_by_a_snapshot_reader(t *testing.T) {
	// Given: a reader holds the destination open the way LoadSnapshot does.
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"v":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openSnapshot(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	tmp := filepath.Join(dir, "credentials.json.tmp")
	if err := os.WriteFile(tmp, []byte(`{"v":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	err = replaceExisting(tmp, path)

	// Then: the path has the new bytes and the open reader keeps the old ones.
	if err != nil {
		t.Fatalf("replaceExisting over an open reader: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != `{"v":"new"}` {
		t.Fatalf("destination = %q, %v", got, err)
	}
	if got, err := io.ReadAll(reader); err != nil || string(got) != `{"v":"old"}` {
		t.Fatalf("open reader saw %q, %v; want the previous bytes", got, err)
	}
}

func Test_replaceExisting_keeps_refusing_a_read_only_destination(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"v":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	tmp := filepath.Join(dir, "credentials.json.tmp")
	if err := os.WriteFile(tmp, []byte(`{"v":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	err := replaceExisting(tmp, path)

	// Then
	if err == nil {
		t.Fatal("replaceExisting replaced a read-only destination")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != `{"v":"old"}` {
		t.Fatalf("destination = %q, %v", got, readErr)
	}
}
