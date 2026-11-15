//go:build !windows

package credstore

import (
	"errors"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func isPlatformNotWritable(err error) bool {
	return errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EROFS)
}

func preflightReplaceable(target string) error {
	if err := unix.Access(target, unix.W_OK); err != nil {
		return wrapNotWritable(target, err)
	}

	var st syscall.Stat_t
	if err := syscall.Stat(target, &st); err != nil {
		return fmtStatErr(target, err)
	}
	if st.Nlink > 1 {
		return wrapNotWritable(target, errors.New("multiply linked credential file"))
	}

	if err := sameMountAsDir(target, filepath.Dir(target)); err != nil {
		return err
	}

	return nil
}

func fmtStatErr(path string, err error) error {
	if isNotWritableFSError(err) {
		return wrapNotWritable(path, err)
	}
	return err
}
