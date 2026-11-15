//go:build !windows

package credstore

import (
	"errors"
	"syscall"
	"testing"
)

func Test_creationNotWritable_classifies_permission_and_read_only_errors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason NotWritableReason
	}{
		{name: "access denied", err: syscall.EACCES, reason: ReasonCannotCreateFiles},
		{name: "operation not permitted", err: syscall.EPERM, reason: ReasonCannotCreateFiles},
		{name: "read-only file system", err: syscall.EROFS, reason: ReasonReadOnlyFilesystem},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := creationNotWritable("/tmp/credentials.json", tc.err)
			var refused *NotWritableError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want NotWritableError", err)
			}
			if refused.Reason != tc.reason {
				t.Fatalf("reason = %v, want %v", refused.Reason, tc.reason)
			}
		})
	}
}
