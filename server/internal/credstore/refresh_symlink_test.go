package credstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_writes_through_symlink_and_locks_beside_target(t *testing.T) {
	// Given
	root := t.TempDir()
	target := filepath.Join(root, "real", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// When
	err := credstore.Refresh(context.Background(), link, func(current []byte) (credstore.Mutator, error) {
		return func(_ []byte) ([]byte, error) {
			return []byte(`{"token":"new"}`), nil
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	assertFileBytesAndMode(t, target, []byte(`{"token":"new"}`), 0o600)
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
	if _, err := os.Stat(target + ".lock"); err != nil {
		t.Fatalf("lock beside target missing: %v", err)
	}
	if _, err := os.Stat(link + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock should not sit beside the symlink path: %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(target))
}

func Test_Refresh_fails_when_path_retargeted_during_prepare(t *testing.T) {
	// Given
	root := t.TempDir()
	a := filepath.Join(root, "a.json")
	b := filepath.Join(root, "b.json")
	if err := os.WriteFile(a, []byte(`{"token":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`{"token":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}

	// When
	err := credstore.Refresh(context.Background(), link, func(current []byte) (credstore.Mutator, error) {
		if err := os.Remove(link); err != nil {
			return nil, err
		}
		if err := os.Symlink(b, link); err != nil {
			return nil, err
		}
		return func(_ []byte) ([]byte, error) {
			return []byte(`{"token":"new"}`), nil
		}, nil
	})

	// Then
	if err == nil {
		t.Fatal("Refresh succeeded after retarget")
	}
	gotA, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != `{"token":"a"}` || string(gotB) != `{"token":"b"}` {
		t.Fatalf("targets mutated: a=%s b=%s", gotA, gotB)
	}
}

func Test_Refresh_resolves_relative_chained_and_directory_links(t *testing.T) {
	// Given
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "credentials.json")
	if err := os.WriteFile(target, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// directory link
	dirLink := filepath.Join(root, "via-dir")
	if err := os.Symlink(realDir, dirLink); err != nil {
		t.Fatal(err)
	}
	// relative file link
	relLink := filepath.Join(root, "rel.json")
	if err := os.Symlink(filepath.Join("real", "credentials.json"), relLink); err != nil {
		t.Fatal(err)
	}
	// chained link
	chain := filepath.Join(root, "chain.json")
	if err := os.Symlink("rel.json", chain); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"directory_link", filepath.Join(dirLink, "credentials.json")},
		{"relative_link", relLink},
		{"chained_link", chain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte(`{"token":"old"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			err := credstore.Refresh(context.Background(), tc.path, func(current []byte) (credstore.Mutator, error) {
				return func(_ []byte) ([]byte, error) {
					return []byte(`{"token":"` + tc.name + `"}`), nil
				}, nil
			})
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != `{"token":"`+tc.name+`"}` {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func Test_Refresh_resolves_link_parent_dotdot_without_lexical_clean(t *testing.T) {
	// Given: path goes through a directory symlink then ".."
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	// File only at root/visible.json, reachable as other/link/../visible.json
	// when link -> secrets. Lexical clean of other/link/../visible.json is
	// other/visible.json (wrong). Correct walk: other -> link(=secrets) -> .. (=root) -> visible.json.
	visible := filepath.Join(root, "visible.json")
	if err := os.WriteFile(visible, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secrets, filepath.Join(other, "link")); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	path := other + sep + "link" + sep + ".." + sep + "visible.json"

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return func(_ []byte) ([]byte, error) {
			return []byte(`{"token":"dotdot"}`), nil
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	assertFileBytesAndMode(t, visible, []byte(`{"token":"dotdot"}`), 0o600)
	wrong := filepath.Join(other, "visible.json")
	if _, err := os.Stat(wrong); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lexical clean wrote the wrong path: %v", err)
	}
}
