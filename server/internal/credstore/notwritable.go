package credstore

import (
	"errors"
	"fmt"
)

// NotWritableReason names why Headroom refused to write a credentials store
// before any token exchange. Reasons come from observed failures only.
type NotWritableReason string

const (
	ReasonReadOnlyFile       NotWritableReason = "read-only file"
	ReasonReadOnlyFilesystem NotWritableReason = "read-only file system"
	ReasonCannotCreateFiles  NotWritableReason = "cannot create files"
	ReasonSeparateMount      NotWritableReason = "separate mount"
	ReasonHardLinks          NotWritableReason = "other hard links"
	ReasonReplacementDenied  NotWritableReason = "replacement denied"
)

// Phrase is the short user-facing explanation for a refusal.
func (r NotWritableReason) Phrase() string {
	switch r {
	case ReasonReadOnlyFile:
		return "the credentials file is read-only"
	case ReasonReadOnlyFilesystem:
		return "the credentials file is on a read-only file system"
	case ReasonCannotCreateFiles:
		return "Headroom cannot create files in the credentials folder"
	case ReasonSeparateMount:
		return "the credentials file is on a separate mount from its folder"
	case ReasonHardLinks:
		return "the credentials file has other hard links"
	case ReasonReplacementDenied:
		return "replacing the credentials file is not permitted"
	default:
		return "the credentials file cannot be replaced"
	}
}

// NotWritableError reports a store that Refresh refused to write before the
// token exchange. Is(ErrNotWritable) holds, and the underlying cause stays
// inspectable through errors.Is.
type NotWritableError struct {
	Path   string
	Reason NotWritableReason
	Err    error
}

func (e *NotWritableError) Error() string {
	return fmt.Sprintf("%s: %s: %s: %v", ErrNotWritable, e.Path, e.Reason, e.Err)
}

// Is matches ErrNotWritable and the underlying cause.
func (e *NotWritableError) Is(target error) bool {
	return target == ErrNotWritable || errors.Is(e.Err, target)
}

// Unwrap reports the refusal sentinel.
func (e *NotWritableError) Unwrap() error { return ErrNotWritable }

// NotWritablePhrase returns the short explanation for a refusal, or a generic
// phrase when err is not a refusal.
func NotWritablePhrase(err error) string {
	var refused *NotWritableError
	if errors.As(err, &refused) {
		return refused.Reason.Phrase()
	}
	return "the credentials file cannot be replaced"
}

func notWritable(path string, reason NotWritableReason, err error) error {
	return &NotWritableError{Path: path, Reason: reason, Err: err}
}

// CreationRefusal classifies a failed lock or directory creation at path.
func CreationRefusal(path string, err error) error {
	return creationNotWritable(path, err)
}
