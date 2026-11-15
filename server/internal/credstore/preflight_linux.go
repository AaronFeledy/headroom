//go:build linux

package credstore

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

var statxMount = unix.Statx
var statMount = syscall.Stat

func sameMountAsDir(target, dir string) error {
	var targetSX, dirSX unix.Statx_t
	targetErr := statxMount(unix.AT_FDCWD, target, 0, unix.STATX_MNT_ID, &targetSX)
	dirErr := statxMount(unix.AT_FDCWD, dir, 0, unix.STATX_MNT_ID, &dirSX)
	if targetErr == nil && dirErr == nil &&
		targetSX.Mask&unix.STATX_MNT_ID != 0 && dirSX.Mask&unix.STATX_MNT_ID != 0 {
		if targetSX.Mnt_id != dirSX.Mnt_id {
			return notWritable(target, ReasonSeparateMount, fmt.Errorf("credential file and directory are on different mounts"))
		}
		return nil
	}

	var targetSt, dirSt syscall.Stat_t
	if err := statMount(target, &targetSt); err != nil {
		return fmtStatErr(target, err)
	}
	if err := statMount(dir, &dirSt); err != nil {
		return fmtStatErr(target, err)
	}
	if targetSt.Dev != dirSt.Dev {
		return notWritable(target, ReasonSeparateMount, fmt.Errorf("credential file and directory are on different devices"))
	}
	return nil
}
