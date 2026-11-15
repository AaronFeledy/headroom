//go:build windows

package credstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_LoadSnapshot_reports_read_only_attribute(t *testing.T) {
	// Given: Windows exposes only the read-only attribute through mode bits.
	path := writeCreds(t, `{"sentinel":"keep"}`, 0o600)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})

	// When
	snapshot, err := credstore.LoadSnapshot(context.Background(), path)

	// Then
	if err != nil {
		t.Fatalf("LoadSnapshot returned error: %v", err)
	}
	if snapshot.Mode.Perm()&0o200 != 0 {
		t.Fatalf("Mode = %v, want read-only", snapshot.Mode.Perm())
	}
}
