package credstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_UpdateFile_creates_missing_non_JSON_file_privately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.yaml")
	err := credstore.UpdateFile(context.Background(), path, 0o600, func(current []byte) ([]byte, error) {
		if current != nil {
			t.Fatal("missing file supplied bytes")
		}
		return []byte("providers: {}\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytesAndMode(t, path, []byte("providers: {}\n"), 0o600)
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode: %v %v", info, err)
	}
}

func Test_UpdateFile_preserves_symlink_and_target_mode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(path, []byte("old: true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	err := credstore.UpdateFile(context.Background(), link, 0o600, func(current []byte) ([]byte, error) {
		return append(current, []byte("new: true\n")...), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytesAndMode(t, path, []byte("old: true\nnew: true\n"), 0o640)
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
}

func Test_UpdateFile_leaves_original_when_context_canceled_before_commit(t *testing.T) {
	// Given
	path := writeCreds(t, `{"external_owner":{"sentinel":"keep"}}`, 0o600)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	// When
	err = credstore.UpdateFile(ctx, path, 0o600, func(current []byte) ([]byte, error) {
		cancel()
		return []byte(`{"external_owner":{"sentinel":"changed"}}`), nil
	})

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateFile error = %v, want context canceled", err)
	}
	assertFileBytesAndMode(t, path, wantBytes, 0o600)
	assertNoTempFiles(t, filepath.Dir(path))
	if err := credstore.UpdateFile(context.Background(), path, 0o600, func(current []byte) ([]byte, error) {
		return current, nil
	}); err != nil {
		t.Fatalf("lock leaked after canceled update: %v", err)
	}
}
