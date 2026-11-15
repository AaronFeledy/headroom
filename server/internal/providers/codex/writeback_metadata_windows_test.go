package codex

import (
	"os"
	"testing"
)

func assertOwnerRestored(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("credential target is not a regular file: %s", path)
	}
}
