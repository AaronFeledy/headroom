package credstore

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func Test_sameMountAsDir_rejects_different_mounts_with_device_fallback(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		mask                                       uint32
		mountDiff, deviceDiff, statxError, refused bool
	}{
		{name: "same mount", mask: unix.STATX_MNT_ID, deviceDiff: true},
		{name: "different mount same device", mask: unix.STATX_MNT_ID, mountDiff: true, refused: true},
		{name: "missing mount id same device"},
		{name: "missing mount id different device", deviceDiff: true, refused: true},
		{name: "unsupported statx same device", statxError: true},
		{name: "unsupported statx different device", statxError: true, deviceDiff: true, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "credentials.json")
			originalStatx, originalStat := statxMount, statMount
			t.Cleanup(func() { statxMount, statMount = originalStatx, originalStat })
			statxMount = func(_ int, path string, _, _ int, stat *unix.Statx_t) error {
				if tc.statxError {
					return unix.ENOSYS
				}
				stat.Mask, stat.Mnt_id = tc.mask, 1
				if path == target && tc.mountDiff {
					stat.Mnt_id = 2
				}
				return nil
			}
			statMount = func(path string, stat *syscall.Stat_t) error {
				stat.Dev = 1
				if path == target && tc.deviceDiff {
					stat.Dev = 2
				}
				return nil
			}
			err := sameMountAsDir(target, dir)
			if errors.Is(err, ErrNotWritable) != tc.refused || (err != nil && !tc.refused) {
				t.Fatalf("mount preflight = %v, refused = %v", err, tc.refused)
			}
		})
	}
}
