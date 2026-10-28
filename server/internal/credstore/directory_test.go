package credstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_DurableDirectory_nested_creation_and_exclusive_namespace(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing", "config", "headroom")
	if err := credstore.EnsureDirectory(context.Background(), parent, 0o700); err != nil {
		t.Fatal(err)
	}
	// Existing ancestors must remain usable when a caller retries initialization.
	if err := credstore.EnsureDirectory(context.Background(), parent, 0o700); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(parent, "state")
	if err := credstore.CreateDirectory(context.Background(), namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(namespace, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := credstore.CreateDirectory(context.Background(), namespace, 0o700); err == nil {
		t.Fatal("exclusive namespace creation accepted an existing directory")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatal("existing namespace changed")
	}
}

func Test_DurableDirectory_failure_never_replaces_existing_file(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(blocker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "headroom", "state")
	if err := credstore.EnsureDirectory(context.Background(), target, 0o700); err == nil {
		t.Fatal("directory creation crossed an existing file")
	}
	if data, err := os.ReadFile(blocker); err != nil || string(data) != "keep" {
		t.Fatal("blocking file changed")
	}
	if err := credstore.CreateDirectory(context.Background(), blocker, 0o700); err == nil {
		t.Fatal("exclusive creation accepted an existing file")
	}
}
