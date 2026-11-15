//go:build windows

package credstore

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_isLockUnsupported_matches_filesystem_capability_errors(t *testing.T) {
	for _, err := range []error{windows.ERROR_INVALID_FUNCTION, windows.ERROR_NOT_SUPPORTED} {
		if !isLockUnsupported(err) {
			t.Fatalf("isLockUnsupported(%v) = false", err)
		}
	}
	for _, err := range []error{windows.ERROR_LOCK_VIOLATION, windows.ERROR_SHARING_VIOLATION, windows.ERROR_ACCESS_DENIED, errors.New("other")} {
		if isLockUnsupported(err) {
			t.Fatalf("isLockUnsupported(%v) = true", err)
		}
	}
}
