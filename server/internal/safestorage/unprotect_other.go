//go:build !windows

package safestorage

func Unprotect([]byte) ([]byte, error) {
	return nil, ErrUnsupported
}
