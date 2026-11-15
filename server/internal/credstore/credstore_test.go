package credstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_LoadSnapshot_reads_bytes_modtime_and_mode(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o640)

	// When
	snapshot, err := credstore.LoadSnapshot(context.Background(), path)

	// Then
	if err != nil {
		t.Fatalf("LoadSnapshot returned error: %v", err)
	}
	if string(snapshot.Data) != `{"sentinel":"keep"}` {
		t.Fatalf("Data = %s", snapshot.Data)
	}
	if !modeMatches(snapshot.Mode, 0o640) {
		t.Fatalf("Mode = %v, want 0640", snapshot.Mode.Perm())
	}
	if snapshot.ModTime.IsZero() {
		t.Fatal("ModTime is zero")
	}
}

func Test_Refresh_returns_error_when_prepare_nil(t *testing.T) {
	// Given
	path := writeCreds(t, `{"external_owner":{"sentinel":"keep"}}`, 0o600)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// When
	err = credstore.Refresh(context.Background(), path, nil)

	// Then
	if !errors.Is(err, credstore.ErrNilMutator) {
		t.Fatalf("Refresh error = %v, want ErrNilMutator", err)
	}
	assertFileBytesAndMode(t, path, wantBytes, 0o600)
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_Refresh_rereads_current_file_under_lock_when_state_changes(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep","first":"old"}`, 0o600)
	ctx := context.Background()
	if err := credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			doc, err := decodeOwnedDoc(latest)
			if err != nil {
				return nil, err
			}
			doc.Fields["first"] = "new"
			return encodePreservingUnknown(latest, doc)
		}, nil
	}); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	// When
	err := credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
		if !strings.Contains(string(current), `"first":"new"`) {
			t.Fatalf("prepare saw stale bytes: %s", string(current))
		}
		return func(latest []byte) ([]byte, error) {
			return latest, nil
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("second Refresh returned error: %v", err)
	}
}

func Test_Refresh_leaves_original_bytes_and_mode_when_prepare_fails(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o640)
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	wantErr := errors.New("boom")

	// When
	err = credstore.Refresh(context.Background(), path, func(_ []byte) (credstore.Mutator, error) {
		return nil, wantErr
	})

	// Then
	if !errors.Is(err, wantErr) {
		t.Fatalf("Refresh error = %v, want boom", err)
	}
	assertFileBytesAndMode(t, path, wantBytes, 0o640)
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_Refresh_rejects_malformed_current_and_edit_output(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)

	// When
	badOutputErr := credstore.Refresh(context.Background(), path, func(_ []byte) (credstore.Mutator, error) {
		return func(_ []byte) ([]byte, error) {
			return []byte(`{"broken"`), nil
		}, nil
	})
	if err := os.WriteFile(path, []byte(`{"broken"`), 0o600); err != nil {
		t.Fatalf("write malformed current: %v", err)
	}
	badInputErr := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			return latest, nil
		}, nil
	})

	// Then
	if !errors.Is(badOutputErr, credstore.ErrInvalidJSON) {
		t.Fatalf("bad output error = %v, want ErrInvalidJSON", badOutputErr)
	}
	if !errors.Is(badInputErr, credstore.ErrInvalidJSON) {
		t.Fatalf("bad input error = %v, want ErrInvalidJSON", badInputErr)
	}
}

func Test_Refresh_honors_context_timeout_while_waiting_for_lock(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)
	block := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
			close(started)
			<-block
			return func(latest []byte) ([]byte, error) {
				return latest, nil
			}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// When
	err := credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			return latest, nil
		}, nil
	})
	close(block)

	// Then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Refresh error = %v, want context deadline", err)
	}
	if firstErr := <-done; firstErr != nil {
		t.Fatalf("blocking Refresh returned error: %v", firstErr)
	}
}

func Test_Refresh_commits_when_context_canceled_after_edit_returned(t *testing.T) {
	// Given
	path := writeCreds(t, `{"token":"old"}`, 0o600)
	ctx, cancel := context.WithCancel(context.Background())

	// When
	err := credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
		cancel()
		return func(_ []byte) ([]byte, error) {
			return []byte(`{"token":"new"}`), nil
		}, nil
	})

	// Then
	if err != nil {
		t.Fatalf("Refresh error = %v, want nil (commit despite cancel)", err)
	}
	assertFileBytesAndMode(t, path, []byte(`{"token":"new"}`), 0o600)
	assertNoTempFiles(t, filepath.Dir(path))
	if err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("lock leaked after canceled-after-edit refresh: %v", err)
	}
}

func Test_ShouldRefresh_returns_reload_when_snapshot_mtime_changed(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	snapshot := credstore.Snapshot{ModTime: now.Add(-time.Minute)}
	state := credstore.RefreshState{Snapshot: snapshot, CurrentModTime: now, ExpiresAt: now.Add(-time.Minute)}

	// When
	decision := credstore.ShouldRefresh(now, state)

	// Then
	if decision != credstore.RefreshDecisionReload {
		t.Fatalf("ShouldRefresh = %v, want reload", decision)
	}
}

func Test_ShouldRefresh_returns_refresh_only_when_expired_and_unchanged(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	snapshot := credstore.Snapshot{ModTime: now}

	// When
	refresh := credstore.ShouldRefresh(now, credstore.RefreshState{Snapshot: snapshot, CurrentModTime: now, ExpiresAt: now.Add(-time.Minute)})
	fresh := credstore.ShouldRefresh(now, credstore.RefreshState{Snapshot: snapshot, CurrentModTime: now, ExpiresAt: now.Add(time.Minute)})

	// Then
	if refresh != credstore.RefreshDecisionRefresh {
		t.Fatalf("expired unchanged decision = %v, want refresh", refresh)
	}
	if fresh != credstore.RefreshDecisionUseSnapshot {
		t.Fatalf("fresh unchanged decision = %v, want use snapshot", fresh)
	}
}
