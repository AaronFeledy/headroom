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

func Test_Refresh_returns_ErrNotWritable_for_read_only_target_without_calling_prepare(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("permission tests unreliable as root")
	}
	// Given
	path := writeCreds(t, `{"token":"old"}`, 0o600)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
	prepareCalled := false

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		prepareCalled = true
		return nil, errors.New("prepare must not run")
	})

	// Then
	if !errors.Is(err, credstore.ErrNotWritable) {
		t.Fatalf("Refresh error = %v, want ErrNotWritable", err)
	}
	if prepareCalled {
		t.Fatal("prepare was called")
	}
	assertFileBytesAndMode(t, path, []byte(`{"token":"old"}`), 0o400)
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_Refresh_returns_ErrNotWritable_for_read_only_directory_without_calling_prepare(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("permission tests unreliable as root")
	}
	// Given
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	prepareCalled := false

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		prepareCalled = true
		return nil, errors.New("prepare must not run")
	})

	// Then
	if !errors.Is(err, credstore.ErrNotWritable) {
		t.Fatalf("Refresh error = %v, want ErrNotWritable", err)
	}
	if prepareCalled {
		t.Fatal("prepare was called")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"token":"old"}` {
		t.Fatalf("bytes = %s err=%v", got, err)
	}
}
