package credstore

import (
	"errors"
	"fmt"
	"testing"
)

func Test_creationNotWritable_classifies_unsupported_file_locking(t *testing.T) {
	// Given: a lock failure from a file system without byte-range locks.
	cause := errors.New("Incorrect function")
	err := creationNotWritable("/mnt/credentials.json", fmt.Errorf("%w: %w", errLockUnsupported, cause))

	// Then
	var refused *NotWritableError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want NotWritableError", err)
	}
	if refused.Reason != ReasonLockUnsupported {
		t.Fatalf("reason = %q, want %q", refused.Reason, ReasonLockUnsupported)
	}
	if !errors.Is(err, ErrNotWritable) {
		t.Fatal("errors.Is(err, ErrNotWritable) = false")
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is(err, cause) = false")
	}
	if got, want := refused.Reason.Phrase(), "Windows can't lock files in this location"; got != want {
		t.Fatalf("phrase = %q, want %q", got, want)
	}
}
