package cli

import (
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/term"
)

func (options Options) defaults() Options {
	if options.tlsSession == nil {
		options.tlsSession = &tlsSession{entries: map[string]*tlsEntry{}}
	}
	if options.Env == nil {
		options.Env = os.Environ()
	}
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 12 * time.Second}
	}
	client := *options.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirects are disabled") }
	options.HTTPClient = &client
	if options.IsTerminal == nil {
		options.IsTerminal = func(writer io.Writer) bool {
			file, ok := writer.(*os.File)
			if !ok {
				return false
			}
			return term.IsTerminal(int(file.Fd()))
		}
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.WatchInterval <= 0 {
		options.WatchInterval = 30 * time.Second
	}
	return options
}
