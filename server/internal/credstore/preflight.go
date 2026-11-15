package credstore

import (
	"errors"
	"os"
)

// creationNotWritable maps a failed lock or temp creation to a typed refusal.
func creationNotWritable(path string, err error) error {
	reason := ReasonCannotCreateFiles
	if isReadOnlyFilesystemError(err) {
		reason = ReasonReadOnlyFilesystem
	}
	return notWritable(path, reason, err)
}

func isNotWritableFSError(err error) bool {
	return errors.Is(err, os.ErrPermission) || isPlatformNotWritable(err)
}
