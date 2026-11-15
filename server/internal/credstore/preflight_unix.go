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

func isReadOnlyFilesystemError(err error) bool {
	return errors.Is(err, syscall.EROFS)
}

func preflightReplaceable(target string) error {
	if err := unix.Access(target, unix.W_OK); err != nil {
		reason := ReasonReadOnlyFile
		if isReadOnlyFilesystemError(err) {
			reason = ReasonReadOnlyFilesystem
		}
		return notWritable(target, reason, err)
	}

	var st syscall.Stat_t
	if err := syscall.Stat(target, &st); err != nil {
		return fmtStatErr(target, err)
	}
	if st.Nlink > 1 {
		return notWritable(target, ReasonHardLinks, errors.New("multiply linked credential file"))
	}

	return sameMountAsDir(target, filepath.Dir(target))
}

func fmtStatErr(path string, err error) error {
	if isNotWritableFSError(err) {
		reason := ReasonReadOnlyFile
		if isReadOnlyFilesystemError(err) {
			reason = ReasonReadOnlyFilesystem
		}
		return notWritable(path, reason, err)
	}
	return err
}
