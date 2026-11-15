package credstore

import (
	"errors"
	"fmt"
	"os"
)

func wrapNotWritable(path string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrNotWritable, path, err)
}

func isNotWritableFSError(err error) bool {
	return errors.Is(err, os.ErrPermission) || isPlatformNotWritable(err)
}
