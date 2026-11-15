//go:build !windows

package tlsidentity

import "os"

func modeMatches(got, want os.FileMode) bool {
	return got.Perm() == want.Perm()
}
