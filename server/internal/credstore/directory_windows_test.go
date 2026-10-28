//go:build windows

package credstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func Test_DurableDirectory_uncertain_publication_never_reuses_visible_destination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headroom")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+directoryPendingSuffix, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirectory(context.Background(), path, 0o700); err == nil {
		t.Fatal("visible directory bypassed uncertain publication marker")
	}
	if err := CreateDirectory(context.Background(), path, 0o700); err == nil {
		t.Fatal("exclusive creation bypassed uncertain publication marker")
	}
}
