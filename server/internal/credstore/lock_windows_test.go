//go:build windows

package credstore

import (
	"errors"
	"syscall"
	"testing"
)

func Test_isLockUnsupported_matches_filesystem_capability_errors(t *testing.T) {
	for _, err := range []error{errorInvalidFunction, errorNotSupported} {
		if !isLockUnsupported(err) {
			t.Fatalf("isLockUnsupported(%v) = false", err)
		}
	}
	for _, err := range []error{errorLockViolation, errorSharingViolation, syscall.ERROR_ACCESS_DENIED, errors.New("other")} {
		if isLockUnsupported(err) {
			t.Fatalf("isLockUnsupported(%v) = true", err)
		}
	}
}
