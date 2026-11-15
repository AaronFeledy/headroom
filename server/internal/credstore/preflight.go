package credstore

import (
	"errors"
	"os"
)

// errLockUnsupported marks a credentials location whose file system cannot
// take the credential file lock. It is a refusal, not a transient failure: a
// refresh there can never save its result.
var errLockUnsupported = errors.New("credential file locking is unsupported at this location")

// creationNotWritable maps a failed lock or temp creation to a typed refusal.
func creationNotWritable(path string, err error) error {
	reason := ReasonCannotCreateFiles
	switch {
	case errors.Is(err, errLockUnsupported):
		reason = ReasonLockUnsupported
	case isReadOnlyFilesystemError(err):
		reason = ReasonReadOnlyFilesystem
	}
	return notWritable(path, reason, err)
}

func isNotWritableFSError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, errLockUnsupported) || isPlatformNotWritable(err)
}
