package credstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_rejects_multiply_linked_target_before_prepare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix link-count policy")
	}
	path := writeCreds(t, `{"token":"old"}`, 0o600)
	other := filepath.Join(filepath.Dir(path), "other.json")
	if err := os.Link(path, other); err != nil {
		t.Fatal(err)
	}
	called := false
	err := credstore.Refresh(context.Background(), path, func([]byte) (credstore.Mutator, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, credstore.ErrNotWritable) || called {
		t.Fatalf("Refresh = %v, prepare called = %v", err, called)
	}
	assertFileBytesAndMode(t, other, []byte(`{"token":"old"}`), 0o600)
}

func Test_Refresh_rechecks_symlink_after_edit_before_rename(t *testing.T) {
	dir := t.TempDir()
	a, b, link := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"), filepath.Join(dir, "link.json")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	err := credstore.Refresh(context.Background(), link, func([]byte) (credstore.Mutator, error) {
		return func([]byte) ([]byte, error) {
			if err := os.Remove(link); err != nil {
				return nil, err
			}
			if err := os.Symlink(b, link); err != nil {
				return nil, err
			}
			return []byte(`{"token":"new"}`), nil
		}, nil
	})
	if err == nil {
		t.Fatal("Refresh accepted a target changed by the edit")
	}
	for _, path := range []string{a, b} {
		assertFileBytesAndMode(t, path, []byte(`{"token":"old"}`), 0o600)
	}
	assertNoTempFiles(t, dir)
}
