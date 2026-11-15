//go:build !windows

package credstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_rejects_symlink_lock_without_modifying_target(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)
	sentinelPath := path + ".sentinel"
	wantSentinel := []byte("sentinel-lock-target")
	if err := os.WriteFile(sentinelPath, wantSentinel, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(sentinelPath, path+".lock"); err != nil {
		t.Fatalf("create lock symlink: %v", err)
	}

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			return latest, nil
		}, nil
	})

	// Then
	if err == nil {
		t.Error("Refresh returned nil error for a symlink lock")
	}
	gotSentinel, readErr := os.ReadFile(sentinelPath)
	if readErr != nil {
		t.Fatalf("read sentinel: %v", readErr)
	}
	if string(gotSentinel) != string(wantSentinel) {
		t.Fatalf("sentinel bytes = %q, want %q", gotSentinel, wantSentinel)
	}
}

func Test_Refresh_rejects_hard_link_lock_without_modifying_target(t *testing.T) {
	// Given
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)
	sentinelPath := path + ".sentinel"
	wantSentinel := []byte("sentinel-lock-target")
	if err := os.WriteFile(sentinelPath, wantSentinel, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Link(sentinelPath, path+".lock"); err != nil {
		t.Fatalf("create lock hard link: %v", err)
	}

	// When
	err := credstore.Refresh(context.Background(), path, func(current []byte) (credstore.Mutator, error) {
		return func(latest []byte) ([]byte, error) {
			return latest, nil
		}, nil
	})

	// Then
	if err == nil {
		t.Error("Refresh returned nil error for a hard-linked lock")
	}
	gotSentinel, readErr := os.ReadFile(sentinelPath)
	if readErr != nil {
		t.Fatalf("read sentinel: %v", readErr)
	}
	if string(gotSentinel) != string(wantSentinel) {
		t.Fatalf("sentinel bytes = %q, want %q", gotSentinel, wantSentinel)
	}
}
