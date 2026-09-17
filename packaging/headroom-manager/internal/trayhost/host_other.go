//go:build !windows

package trayhost

import (
	"errors"
	"io"
)

func Run(input io.Reader, output io.Writer, launcher, parentExecutable string) error {
	return errors.New("the persistent tray host is Windows-only")
}
