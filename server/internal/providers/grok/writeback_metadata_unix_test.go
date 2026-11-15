//go:build !windows

package grok

import (
	"os"
	"syscall"
	"testing"
)

func assertOwnerRestored(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("missing file ownership for %s", path)
	}
	if int(stat.Uid) != os.Getuid() {
		t.Fatalf("%s uid = %d, want %d", path, stat.Uid, os.Getuid())
	}
}
