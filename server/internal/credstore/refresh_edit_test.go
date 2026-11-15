package credstore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_applies_edit_to_bytes_changed_during_prepare(t *testing.T) {
	// Given
	path := writeCreds(t, `{"keep":"yes","token":"old"}`, 0o600)

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		// Unrelated writer changes a field while prepare runs (no Headroom lock).
		if err := os.WriteFile(path, []byte(`{"keep":"yes","token":"old","extra":"mid"}`), 0o600); err != nil {
			return nil, err
		}
		return func(latest []byte) ([]byte, error) {
			var doc map[string]string
			if err := json.Unmarshal(latest, &doc); err != nil {
				return nil, err
			}
			if doc["extra"] != "mid" {
				t.Fatalf("edit did not see mid-prepare write: %s", latest)
			}
			doc["token"] = "new"
			return json.Marshal(doc)
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]string
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["token"] != "new" || doc["extra"] != "mid" || doc["keep"] != "yes" {
		t.Fatalf("final = %s", got)
	}
}

func Test_Refresh_nil_edit_leaves_inode_and_mtime_unchanged(t *testing.T) {
	// Given
	path := writeCreds(t, `{"token":"same"}`, 0o600)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return nil, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("nil edit replaced the file inode")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_Refresh_identical_output_leaves_inode_and_mtime_unchanged(t *testing.T) {
	// Given
	body := `{"token":"same"}`
	path := writeCreds(t, body, 0o600)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// When
	err = credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			out := append([]byte(nil), latest...)
			return out, nil
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("identical output replaced the file inode")
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	assertNoTempFiles(t, filepath.Dir(path))
}
