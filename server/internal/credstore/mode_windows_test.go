//go:build windows

package credstore_test

import "os"

// Windows has no Unix permission bits: Go reports 0666 for a writable file and
// 0444 once the read-only attribute is set, so only the write bit is meaningful.
func modeMatches(got, want os.FileMode) bool {
	return got.Perm()&0o200 == want.Perm()&0o200
}
