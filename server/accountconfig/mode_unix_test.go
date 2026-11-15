//go:build !windows

package accountconfig_test

import "os"

func modeMatches(got, want os.FileMode) bool {
	return got.Perm() == want.Perm()
}
