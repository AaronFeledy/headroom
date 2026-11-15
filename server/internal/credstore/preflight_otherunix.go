//go:build !windows && !linux

package credstore

import (
	"fmt"
	"syscall"
)

func sameMountAsDir(target, dir string) error {
	var targetSt, dirSt syscall.Stat_t
	if err := syscall.Stat(target, &targetSt); err != nil {
		return fmtStatErr(target, err)
	}
	if err := syscall.Stat(dir, &dirSt); err != nil {
		return fmtStatErr(target, err)
	}
	if targetSt.Dev != dirSt.Dev {
		return wrapNotWritable(target, fmt.Errorf("credential file and directory are on different devices"))
	}
	return nil
}
