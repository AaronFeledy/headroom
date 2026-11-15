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

func Test_NotWritableError_matches_sentinel_and_cause(t *testing.T) {
	refused := &credstore.NotWritableError{
		Path:   "/tmp/credentials.json",
		Reason: credstore.ReasonReplacementDenied,
		Err:    os.ErrPermission,
	}
	if !errors.Is(refused, credstore.ErrNotWritable) {
		t.Fatal("errors.Is(refused, ErrNotWritable) = false")
	}
	if !errors.Is(refused, os.ErrPermission) {
		t.Fatal("errors.Is(refused, os.ErrPermission) = false")
	}
	if got := credstore.NotWritablePhrase(refused); got != "replacing the credentials file is not permitted" {
		t.Fatalf("phrase = %q", got)
	}
}

func Test_NotWritableReason_phrases_are_distinct(t *testing.T) {
	reasons := []credstore.NotWritableReason{
		credstore.ReasonReadOnlyFile,
		credstore.ReasonReadOnlyFilesystem,
		credstore.ReasonCannotCreateFiles,
		credstore.ReasonSeparateMount,
		credstore.ReasonHardLinks,
		credstore.ReasonReplacementDenied,
		credstore.ReasonLockUnsupported,
	}
	seen := map[string]bool{}
	for _, reason := range reasons {
		phrase := reason.Phrase()
		if phrase == "" || seen[phrase] {
			t.Fatalf("reason %q has an empty or duplicate phrase %q", reason, phrase)
		}
		seen[phrase] = true
	}
}

func Test_NotWritablePhrase_uses_generic_explanation_without_known_reason(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "nil"},
		{name: "unrelated error", err: errors.New("unrelated")},
		{name: "unknown reason", err: &credstore.NotWritableError{Reason: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credstore.NotWritablePhrase(tc.err); got != "the credentials file cannot be replaced" {
				t.Fatalf("phrase = %q", got)
			}
		})
	}
}

func Test_Refresh_reports_read_only_file_reason(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix permission test")
	}
	path := writeCreds(t, `{"token":"old"}`, 0o600)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
	err := credstore.Refresh(context.Background(), path, func([]byte) (credstore.Mutator, error) {
		return nil, errors.New("prepare must not run")
	})
	reason, ok := refusalReason(err)
	if !ok || reason != credstore.ReasonReadOnlyFile {
		t.Fatalf("err = %v, reason = %v, want %v", err, reason, credstore.ReasonReadOnlyFile)
	}
}

func Test_Refresh_reports_cannot_create_files_reason(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix permission test")
	}
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
	err := credstore.Refresh(context.Background(), path, func([]byte) (credstore.Mutator, error) {
		return nil, errors.New("prepare must not run")
	})
	reason, ok := refusalReason(err)
	if !ok || reason != credstore.ReasonCannotCreateFiles {
		t.Fatalf("err = %v, reason = %v, want %v", err, reason, credstore.ReasonCannotCreateFiles)
	}
}

func Test_Refresh_reports_hard_link_reason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix link-count policy")
	}
	path := writeCreds(t, `{"token":"old"}`, 0o600)
	if err := os.Link(path, filepath.Join(filepath.Dir(path), "other.json")); err != nil {
		t.Fatal(err)
	}
	err := credstore.Refresh(context.Background(), path, func([]byte) (credstore.Mutator, error) {
		return nil, errors.New("prepare must not run")
	})
	reason, ok := refusalReason(err)
	if !ok || reason != credstore.ReasonHardLinks {
		t.Fatalf("err = %v, reason = %v, want %v", err, reason, credstore.ReasonHardLinks)
	}
}

func refusalReason(err error) (credstore.NotWritableReason, bool) {
	var refused *credstore.NotWritableError
	if !errors.As(err, &refused) {
		return "", false
	}
	return refused.Reason, true
}
