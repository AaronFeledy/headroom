package main

import (
	"errors"
	"os"
	"testing"
)

func TestManagedAccountsServiceReadAbsentInstallRoot(t *testing.T) {
	t.Setenv("HEADROOM_INSTALL_ROOT", "")
	_, err := managedAccountsService{}.Read()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read error = %v, want os.ErrNotExist", err)
	}
}
