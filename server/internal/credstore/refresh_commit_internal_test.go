package credstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type commitTokenDoc struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Marker  string `json:"marker,omitempty"`
}

func commitTokenCodec(t *testing.T, edits *int) TokenCodec[commitTokenDoc] {
	t.Helper()
	return TokenCodec[commitTokenDoc]{
		Pair: func(file []byte) (TokenPair, error) {
			doc, err := decodeCommitDoc(file)
			if err != nil {
				return TokenPair{}, err
			}
			return TokenPair{Access: doc.Access, Refresh: doc.Refresh}, nil
		},
		Apply: func(file []byte, tokens commitTokenDoc) ([]byte, error) {
			*edits++
			doc, err := decodeCommitDoc(file)
			if err != nil {
				return nil, err
			}
			doc.Access, doc.Refresh = tokens.Access, tokens.Refresh
			return json.Marshal(doc)
		},
		Of: func(tokens commitTokenDoc) TokenPair {
			return TokenPair{Access: tokens.Access, Refresh: tokens.Refresh}
		},
	}
}

func decodeCommitDoc(data []byte) (commitTokenDoc, error) {
	var doc commitTokenDoc
	err := json.Unmarshal(data, &doc)
	return doc, err
}

func writeCommitCreds(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return path
}

func readCommitCreds(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	return string(data)
}

func replaceCommitVars(t *testing.T, replace func(string, string) error, delay bool) {
	t.Helper()
	originalReplace := replaceCredential
	originalSync := syncCredentialDirectory
	originalDelay := commitRetryBaseDelay
	if delay {
		commitRetryBaseDelay = 0
	}
	replaceCredential = replace
	t.Cleanup(func() {
		replaceCredential = originalReplace
		syncCredentialDirectory = originalSync
		commitRetryBaseDelay = originalDelay
	})
}

func Test_RefreshTokens_reports_saved_when_successor_landed_before_commit_error(t *testing.T) {
	// Given: the rename lands but directory sync fails afterwards.
	path := writeCommitCreds(t, `{"access":"old","refresh":"old-r"}`)
	replaceCommitVars(t, replaceExisting, false)
	syncCredentialDirectory = func(string) error { return errors.New("sync failed") }
	warns := 0
	edits := 0
	codec := commitTokenCodec(t, &edits)
	codec.Warn = func(error) { warns++ }

	// When
	_, outcome, err := RefreshTokens(context.Background(), path,
		TokenPair{Access: "old", Refresh: "old-r"}, codec,
		func([]byte) (commitTokenDoc, error) {
			return commitTokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err != nil || outcome != Saved {
		t.Fatalf("err=%v outcome=%v, want Saved", err, outcome)
	}
	got := readCommitCreds(t, path)
	if !strings.Contains(got, `"access":"new"`) || !strings.Contains(got, `"refresh":"new-r"`) {
		t.Fatalf("successor not on disk: %s", got)
	}
	if warns != 1 {
		t.Fatalf("warns = %d, want 1", warns)
	}
}

func Test_Refresh_retries_commit_on_fresh_bytes_and_succeeds(t *testing.T) {
	// Given: the first two renames fail after another writer changes the file.
	path := writeCommitCreds(t, `{"access":"old","refresh":"old-r","marker":"original"}`)
	calls := 0
	replaceCommitVars(t, func(tmpName, target string) error {
		calls++
		if calls <= 2 {
			if err := os.WriteFile(target, []byte(`{"access":"old","refresh":"old-r","marker":"fresh"}`), 0o600); err != nil {
				return err
			}
			return errors.New("rename failed")
		}
		return replaceExisting(tmpName, target)
	}, true)
	edits := 0

	// When
	_, outcome, err := RefreshTokens(context.Background(), path,
		TokenPair{Access: "old", Refresh: "old-r"}, commitTokenCodec(t, &edits),
		func([]byte) (commitTokenDoc, error) {
			return commitTokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err != nil || outcome != Saved {
		t.Fatalf("err=%v outcome=%v", err, outcome)
	}
	if calls != 3 || edits != 3 {
		t.Fatalf("rename calls = %d, edit calls = %d, want 3 and 3", calls, edits)
	}
	got := readCommitCreds(t, path)
	if !strings.Contains(got, `"marker":"fresh"`) || !strings.Contains(got, `"access":"new"`) {
		t.Fatalf("edit did not re-apply to fresh bytes: %s", got)
	}
	assertNoCommitTempFiles(t, filepath.Dir(path))
}

func Test_Refresh_gives_up_after_commit_retry_bound(t *testing.T) {
	// Given: every rename fails.
	path := writeCommitCreds(t, `{"access":"old","refresh":"old-r"}`)
	calls := 0
	replaceCommitVars(t, func(string, string) error {
		calls++
		return errors.New("rename failed")
	}, true)
	edits := 0

	// When
	_, _, err := RefreshTokens(context.Background(), path,
		TokenPair{Access: "old", Refresh: "old-r"}, commitTokenCodec(t, &edits),
		func([]byte) (commitTokenDoc, error) {
			return commitTokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err == nil {
		t.Fatal("RefreshTokens succeeded despite failed renames")
	}
	if calls != 5 || edits != 5 {
		t.Fatalf("rename calls = %d, edit calls = %d, want 5 and 5", calls, edits)
	}
	if got := readCommitCreds(t, path); got != `{"access":"old","refresh":"old-r"}` {
		t.Fatalf("file changed: %s", got)
	}
	assertNoCommitTempFiles(t, filepath.Dir(path))
}

func assertNoCommitTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp artifact remains: %s", entry.Name())
		}
	}
}
