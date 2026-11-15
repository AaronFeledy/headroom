//go:build !windows

package credstore

import "os"

func openSnapshot(path string) (*os.File, error) {
	return os.Open(path)
}
