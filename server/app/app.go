// Package app runs the Headroom usage API server in the calling process.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/user"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/claude"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/codex"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/cursor"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/grok"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/server"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/sshaccess"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// Run serves until ctx is canceled or a server component fails. Args and env
// use the same syntax and precedence as the usage-server executable. Input and
// output carry the SSH stdio or private desktop-session protocols when selected.
func Run(ctx context.Context, args, env []string, logger *slog.Logger, version string,
	input io.Reader, output io.Writer) error {
	return runContext(ctx, args, env, logger, desktopSessionOptions{
		input: input, output: output, version: version,
	})
}

// RunWithReady is Run with an in-process startup acknowledgement. Ready is
// called once after configuration, provider construction, and all listeners
// succeed, before polling starts. Returning an error closes those listeners and
// aborts startup. Help and SSH stdio requests never acknowledge a server start.
func RunWithReady(ctx context.Context, args, env []string, logger *slog.Logger, version string,
	input io.Reader, output io.Writer, ready func() error) error {
	return runContext(ctx, args, env, logger, desktopSessionOptions{
		input: input, output: output, version: version, ready: ready,
	})
}

func runContext(ctx context.Context, args []string, env []string, logger *slog.Logger, desktopOptions desktopSessionOptions) error {
	desktopOptions = desktopOptions.withDefaults()
	stdioMode, err := sshaccess.IsStdioMode(args)
	if err != nil {
		return err
	}
	if stdioMode {
		if err := sshaccess.ValidatePlatform(); err != nil {
			return err
		}
		home, err := runtimeHome(desktopOptions)
		if err != nil {
			return err
		}
		return sshaccess.ServeStdio(ctx, desktopOptions.input, desktopOptions.output, home)
	}
	cfg, err := config.Load(ctx, config.LoadOptions{Args: args, Env: env, Logger: logger})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if cfg.DesktopSession {
		if cfg.SSHAccess {
			return fmt.Errorf("desktop-session and ssh-access cannot be combined: %w", config.ErrInvalidConfig)
		}
		return runDesktopSession(ctx, cfg, logger, desktopOptions)
	}
	if cfg.SSHAccess {
		if err := sshaccess.ValidatePlatform(); err != nil {
			return err
		}
	}
	if err := api.ValidateStartup(cfg.ListenAddr, cfg.AuthToken); err != nil {
		return err
	}
	var sshListener net.Listener
	if cfg.SSHAccess {
		home, err := runtimeHome(desktopOptions)
		if err != nil {
			return err
		}
		sshListener, err = sshaccess.Listen(home)
		if err != nil {
			return err
		}
		defer sshListener.Close()
	}
	providerPoller, codexClient, cursorClient, grokProvider, names, err := buildPoller(cfg)
	if err != nil {
		return err
	}
	handler := api.NewHandler(api.Options{
		Cache:         providerPoller,
		Cursor:        cursorClient,
		Grok:          grokProvider,
		Codex:         codexClient,
		Poller:        providerPoller,
		Logger:        logger,
		AuthToken:     cfg.AuthToken,
		Version:       desktopOptions.version,
		ProviderNames: names,
	})
	listener, err := desktopOptions.listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := acknowledgeReady(ctx, desktopOptions.ready); err != nil {
		return err
	}
	servers := []server.RunOptions{{Listener: listener, Handler: handler, Logger: logger}}
	if sshListener != nil {
		servers = append(servers, server.RunOptions{Listener: sshListener, Handler: sshaccess.InjectAuthorization(cfg.AuthToken, handler), Logger: logger})
	}
	return runServerAndPoller(ctx, appRuntime{servers: servers, poller: providerPoller, interval: cfg.PollInterval})
}

func runDesktopSession(ctx context.Context, cfg config.Config, logger *slog.Logger, options desktopSessionOptions) error {
	ctx, stopParentWatch := desktopParentContext(ctx)
	defer stopParentWatch()
	prepared, err := prepareDesktopSession(ctx, cfg.ListenAddr, options)
	if err != nil {
		return err
	}
	defer prepared.listener.Close()
	cfg.AuthToken = prepared.request.Token
	providerPoller, codexClient, cursorClient, grokProvider, names, err := buildPoller(cfg)
	if err != nil {
		return err
	}
	handler := api.NewHandler(api.Options{
		Cache:         providerPoller,
		Cursor:        cursorClient,
		Grok:          grokProvider,
		Codex:         codexClient,
		Poller:        providerPoller,
		Logger:        logger,
		AuthToken:     cfg.AuthToken,
		Version:       options.version,
		ProviderNames: names,
	})
	if err := prepared.publishIdentity(); err != nil {
		return err
	}
	if err := acknowledgeReady(ctx, options.ready); err != nil {
		return err
	}
	return runServerAndPoller(ctx, appRuntime{
		servers:  []server.RunOptions{{Listener: prepared.listener, Handler: handler, Logger: logger}},
		poller:   providerPoller,
		interval: cfg.PollInterval,
	})
}

func acknowledgeReady(ctx context.Context, ready func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		return ready()
	}
	return nil
}

type appRuntime struct {
	servers  []server.RunOptions
	poller   *poller.Poller
	interval time.Duration
}

func buildPoller(cfg config.Config) (*poller.Poller, *codex.Client, *cursor.Client, *grok.Provider, []string, error) {
	allowLocalDiscovery, err := api.IsLoopbackListenAddr(cfg.ListenAddr)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	providerPoller := poller.New(poller.Options{})
	var cursorClient *cursor.Client
	var codexClient *codex.Client
	var grokProvider *grok.Provider
	names := []string{}
	for name, providerCfg := range cfg.Providers {
		if !providerCfg.Enabled {
			continue
		}
		provider, err := buildProvider(name, providerCfg, allowLocalDiscovery)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		if c, ok := provider.(*cursor.Client); ok {
			cursorClient = c
		}
		if c, ok := provider.(*codex.Client); ok {
			codexClient = c
		}
		if g, ok := provider.(*grok.Provider); ok {
			grokProvider = g
		}
		if err := providerPoller.Register(provider, true); err != nil {
			return nil, nil, nil, nil, nil, err
		}
		names = append(names, provider.Name())
	}
	return providerPoller, codexClient, cursorClient, grokProvider, names, nil
}

func buildProvider(name string, providerCfg config.ProviderConfig, allowLocalDiscovery bool) (usage.Provider, error) {
	switch strings.ToLower(name) {
	case "claude":
		return claude.New(claude.Options{CredentialsPath: providerCfg.CredentialsPath}), nil
	case "codex":
		return codex.New(codex.Options{CredentialsPath: providerCfg.CredentialsPath}), nil
	case "cursor":
		return cursor.NewClient(cursor.Options{AuthPath: providerCfg.CredentialsPath, AllowLocalDiscovery: allowLocalDiscovery}), nil
	case "grok":
		return grok.NewProvider(grok.Options{CredentialsPath: providerCfg.CredentialsPath})
	default:
		return nil, fmt.Errorf("unknown provider %q: %w", name, config.ErrInvalidConfig)
	}
}

func runServerAndPoller(ctx context.Context, runtime appRuntime) error {
	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(runtime.servers)+1)
	go func() {
		err := runtime.poller.Run(appCtx, runtime.interval)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		errCh <- err
	}()
	for _, options := range runtime.servers {
		options := options
		go func() { errCh <- server.Run(appCtx, options) }()
	}
	first := <-errCh
	cancel()
	var shutdownErr error
	for range runtime.servers {
		if err := <-errCh; shutdownErr == nil && err != nil {
			shutdownErr = err
		}
	}
	if first != nil {
		return first
	}
	return shutdownErr
}

func runtimeHome(options desktopSessionOptions) (string, error) {
	if options.homeDir != "" {
		return options.homeDir, nil
	}
	current, err := user.Current()
	if err != nil || current.HomeDir == "" {
		return "", fmt.Errorf("resolve current account home")
	}
	return current.HomeDir, nil
}
