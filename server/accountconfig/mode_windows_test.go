//go:build windows

package accountconfig_test

import "os"

// Windows reports 0666 for a writable file and 0444 once the read-only
// attribute is set, so only the write bit is meaningful.
func modeMatches(got, want os.FileMode) bool {
	return got.Perm()&0o200 == want.Perm()&0o200
}
