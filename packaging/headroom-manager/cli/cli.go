// Package cli implements the public Headroom command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

type CommandFunc func(context.Context, []string) error
type ServeFunc func(context.Context, []string, []string, *slog.Logger, string, io.Reader, io.Writer) error
type SSHFunc func(context.Context, string, []string, []byte) ([]byte, []byte, error)
type LocalSnapshot struct {
	Body     []byte
	Status   string
	LastGood time.Time
}
type LocalFetchFunc func(context.Context) (LocalSnapshot, error)

// Only absence of an existing desktop permits ordinary localhost discovery.
// A failed private bridge must never silently change the selected transport.
var ErrLocalUnavailable = errors.New("local desktop is not running")

type Options struct {
	Args, Env             []string
	Stdin                 io.Reader
	Stdout, Stderr        io.Writer
	Version               string
	HTTPClient            *http.Client
	Now                   func() time.Time
	IsTerminal            func(io.Writer) bool
	WatchInterval         time.Duration
	RunSSH                SSHFunc
	FetchLocal            LocalFetchFunc
	Serve                 ServeFunc
	Update, Desktop, Pair CommandFunc
	tlsSession            *tlsSession
}

func Run(ctx context.Context, options Options) error {
	options = options.defaults()
	defer func() {
		for _, entry := range options.tlsSession.entries {
			entry.client.CloseIdleConnections()
		}
	}()
	if len(options.Args) > 0 {
		// Older Linux desktop autostart entries invoked the public command with
		// --background. Keep that exact leading argument on the desktop path now
		// that the public command is the CLI.
		if options.Args[0] == "--background" {
			if options.Desktop == nil {
				return errors.New("desktop launch is unavailable in this Headroom build")
			}
			return options.Desktop(ctx, options.Args)
		}
		switch options.Args[0] {
		case "serve":
			if options.Serve == nil {
				return errors.New("serve is unavailable in this Headroom build")
			}
			logger := slog.New(slog.NewTextHandler(options.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			return options.Serve(ctx, options.Args[1:], options.Env, logger, options.Version, options.Stdin, options.Stdout)
		case "update", "self-update":
			if options.Update == nil {
				return errors.New("update is unavailable in this Headroom build")
			}
			return options.Update(ctx, options.Args[1:])
		case "desktop":
			if options.Desktop == nil {
				return errors.New("desktop launch is unavailable in this Headroom build")
			}
			return options.Desktop(ctx, options.Args[1:])
		case "pair":
			if options.Pair == nil {
				return errors.New("pairing is unavailable in this Headroom build")
			}
			return options.Pair(ctx, options.Args[1:])
		case "version":
			if len(options.Args) != 1 {
				return errors.New("version does not accept arguments")
			}
			_, err := fmt.Fprintln(options.Stdout, options.Version)
			return err
		case "help":
			if len(options.Args) != 1 {
				return errors.New("help does not accept arguments")
			}
			options.Args = []string{"--help"}
		}
	}
	return runDashboard(ctx, options)
}

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

type dashboardFlags struct {
	once, watch, json, plain, weeklyOnly bool
	url, ssh                             string
}

func runDashboard(ctx context.Context, options Options) error {
	set := flag.NewFlagSet("headroom", flag.ContinueOnError)
	set.SetOutput(options.Stderr)
	var flags dashboardFlags
	set.BoolVar(&flags.once, "once", false, "print one snapshot and exit")
	set.BoolVar(&flags.watch, "watch", false, "refresh until interrupted")
	set.BoolVar(&flags.json, "json", false, "write the API response as JSON")
	set.BoolVar(&flags.plain, "plain", false, "disable color and terminal refresh controls")
	set.BoolVar(&flags.weeklyOnly, "weekly-only", false, "show weekly meters only")
	set.StringVar(&flags.url, "url", "", "HTTP(S) server base URL")
	set.StringVar(&flags.ssh, "ssh", "", "SSH destination ([user@]host[:port])")
	showVersion := set.Bool("version", false, "print Headroom version")
	set.Usage = func() {
		fmt.Fprintln(options.Stderr, "Usage: headroom [dashboard options]\n       headroom serve [server options]\n       headroom update [--this-install-only [--version VERSION] | --reconcile]\n       headroom desktop\n       headroom pair windows-wsl [pairing options]\n       headroom version")
		set.PrintDefaults()
		fmt.Fprintln(options.Stderr, "Remote HTTP authentication: HEADROOM_AUTH_TOKEN or HEADROOM_AUTH_TOKEN_FILE")
		fmt.Fprintln(options.Stderr, "Token files require owner-only permissions on Linux/macOS; use HEADROOM_AUTH_TOKEN on Windows.")
	}
	if err := set.Parse(options.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(options.Stdout, options.Version)
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", set.Arg(0))
	}
	if flags.once && flags.watch {
		return errors.New("--once and --watch cannot be combined")
	}
	if flags.url != "" && flags.ssh != "" {
		return errors.New("--url and --ssh cannot be combined")
	}
	terminal := options.IsTerminal(options.Stdout) && envValue(options.Env, "TERM") != "dumb"
	terminal, restoreTerminal := prepareDashboardTerminal(options.Stdout, terminal)
	defer restoreTerminal()
	color := terminal && !flags.plain && !envPresent(options.Env, "NO_COLOR")
	watch := flags.watch || (!flags.once && !flags.plain && !flags.json && terminal)
	warnings := make(map[string]warningState)
	var lastUsage []Provider
	var lastGood time.Time
	haveSnapshot := false
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		snapshot, usage, fetchErr := fetchUsage(ctx, options, flags)
		if ctx.Err() != nil {
			return nil
		}
		now := options.Now()
		offline := fetchErr != nil || (snapshot.Status != "" && snapshot.Status != "ready")
		if fetchErr != nil && !watch {
			return fetchErr
		}
		if fetchErr == nil {
			lastUsage, haveSnapshot = usage, len(usage) > 0 || !offline
			if !snapshot.LastGood.IsZero() {
				lastGood = snapshot.LastGood
			} else if !offline {
				lastGood = now
			}
		}
		if watch && terminal && !flags.plain && !flags.json {
			if _, err := fmt.Fprint(options.Stdout, "\x1b[H\x1b[2J"); err != nil {
				return err
			}
		}
		if offline {
			destination := options.Stdout
			if flags.json {
				destination = options.Stderr
			}
			message := "Usage unavailable"
			if haveSnapshot && !lastGood.IsZero() {
				message = fmt.Sprintf("Offline · last good readings %s ago", now.Sub(lastGood).Round(time.Second))
			}
			if watch {
				message += " · retrying"
			}
			if _, err := fmt.Fprintln(destination, message); err != nil {
				return err
			}
		}
		if flags.json {
			// Emit only valid API snapshots. Retry diagnostics stay on stderr.
			if fetchErr == nil {
				if _, err := options.Stdout.Write(append(snapshot.Body, '\n')); err != nil {
					return err
				}
			}
		} else if haveSnapshot {
			if _, err := io.WriteString(options.Stdout, render(lastUsage, now, color, warnings)); err != nil {
				return err
			}
		}
		if !watch {
			return nil
		}
		timer := time.NewTimer(options.WatchInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func envPresent(env []string, name string) bool {
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return true
		}
	}
	return false
}

func envValue(env []string, name string) string {
	for index := len(env) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(env[index], "=")
		if found && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return value
		}
	}
	return ""
}
