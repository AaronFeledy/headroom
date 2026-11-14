package credstore_test

import (
	"context"
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
