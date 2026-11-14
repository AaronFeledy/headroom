package app

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/claude"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/codex"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/cursor"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/grok"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/opencodego"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func buildPoller(cfg config.Config, logger *slog.Logger) (*poller.Poller, *codex.Client, *claude.Client, *cursor.Client, *grok.Provider, []string, error) {
	return buildPollerWithDiagnostics(cfg, nil, logger)
}

func buildPollerWithCursorDiscovery(cfg config.Config, discovery *cursor.Discovery) (*poller.Poller, *codex.Client, *claude.Client, *cursor.Client, *grok.Provider, []string, error) {
	return buildPollerWithDiagnostics(cfg, discovery, nil)
}

func buildPollerWithDiagnostics(cfg config.Config, discovery *cursor.Discovery, logger *slog.Logger) (*poller.Poller, *codex.Client, *claude.Client, *cursor.Client, *grok.Provider, []string, error) {
	providerPoller := poller.New(poller.Options{Logger: logger})
	var cursorClient *cursor.Client
	var codexClient *codex.Client
	var claudeClient *claude.Client
	var grokProvider *grok.Provider
	names := []string{}
	for name, providerCfg := range cfg.Providers {
		if providerCfg.Enabled {
			provider, err := buildProvider(name, providerCfg, cfg.ResetStateDir)
			if err != nil {
				return nil, nil, nil, nil, nil, nil, err
			}
			if c, ok := provider.(*cursor.Client); ok {
				if discovery != nil {
					c = cursor.NewClient(cursor.Options{AuthPath: providerCfg.CredentialsPath, Discovery: discovery})
					provider = c
				}
				cursorClient = c
			}
			if c, ok := provider.(*codex.Client); ok {
				codexClient = c
			}
			if c, ok := provider.(*claude.Client); ok {
				claudeClient = c
			}
			if g, ok := provider.(*grok.Provider); ok {
				grokProvider = g
			}
			if err := providerPoller.Register(provider, true); err != nil {
				return nil, nil, nil, nil, nil, nil, err
			}
			names = append(names, provider.Name())
		}
		if strings.ToLower(name) != "claude" {
			continue
		}
		for _, account := range providerCfg.Accounts {
			if !account.Enabled {
				continue
			}
			extra := claude.New(claude.Options{
				ConfigDir: account.ConfigDir,
				Keychain:  claudeKeychain(),
				Name:      "Claude (" + account.Label + ")",
				Account:   &usage.Account{ID: account.ID, Label: account.Label},
			})
			if err := providerPoller.Register(extra, true); err != nil {
				return nil, nil, nil, nil, nil, nil, err
			}
			names = append(names, extra.Name())
		}
	}
	return providerPoller, codexClient, claudeClient, cursorClient, grokProvider, names, nil
}

func buildProvider(name string, providerCfg config.ProviderConfig, resetStateDir string) (usage.Provider, error) {
	switch strings.ToLower(name) {
	case "claude":
		return claude.New(claude.Options{CredentialsPath: providerCfg.CredentialsPath, Keychain: claudeKeychain(), ResetStateDir: resetStateDir}), nil
	case "codex":
		return codex.New(codex.Options{CredentialsPath: providerCfg.CredentialsPath}), nil
	case "cursor":
		return cursor.NewClient(cursor.Options{AuthPath: providerCfg.CredentialsPath, BrowserCredentials: &providerCfg.BrowserCredentials}), nil
	case "grok":
		return grok.NewProvider(grok.Options{CredentialsPath: providerCfg.CredentialsPath})
	case "opencodego":
		return opencodego.NewProvider(opencodego.Options{CredentialsPath: providerCfg.CredentialsPath}), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: %w", name, config.ErrInvalidConfig)
	}
}
