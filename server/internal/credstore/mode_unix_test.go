//go:build !windows

package credstore_test

import "os"

func modeMatches(got, want os.FileMode) bool {
	return got.Perm() == want.Perm()
}
