package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	providerName      = "Claude"
	primaryLabel      = "Current Session"
	secondaryLabel    = "Weekly"
	defaultUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	defaultTokenURL   = "https://platform.claude.com/v1/oauth/token"
	betaHeader        = "oauth-2025-04-20"
	userAgent         = "claude-code/2.1.69"
	oauthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultTimeout    = 15 * time.Second
	expirySkew        = 60 * time.Second
	reauthCommandText = "claude auth login"
)

type Options struct {
	CredentialsPath string
	HomeDir         string
	WSLHomeDir      string
	OpenCodePath    string
	UsageURL        string
	TokenURL        string
	HTTPClient      *http.Client
}

type Client struct {
	client   *http.Client
	usageURL string
	tokenURL string
	paths    discoveryOptions

	stateMu   sync.Mutex
	refreshMu sync.Mutex
	loaded    *credentials
}

func New(opts Options) *Client {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	usageURL := opts.UsageURL
	if usageURL == "" {
		usageURL = defaultUsageURL
	}
	tokenURL := opts.TokenURL
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	return &Client{
		client:   client,
		usageURL: usageURL,
		tokenURL: tokenURL,
		paths: discoveryOptions{
			configuredPath: opts.CredentialsPath,
			homeDir:        opts.HomeDir,
			wslHomeDir:     opts.WSLHomeDir,
			openCodePath:   opts.OpenCodePath,
		},
	}
}

func (c *Client) Name() string { return providerName }

func (c *Client) Fetch(ctx context.Context) (usage.UsageData, error) {
	creds, err := c.currentCredentials(ctx)
	if err != nil {
		return reauthUsageData(formatCredentialError(err)), err
	}
	for attempt := 0; attempt < 2; attempt++ {
		ready, refreshErr := c.ensureFreshCredentials(ctx, creds)
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrInvalidGrant) {
				return reauthUsageData("AUTH_EXPIRED"), nil
			}
			return errorUsageData("Token refresh failed. Will retry."), nil
		}
		creds = ready
		data, statusErr, err := c.fetchUsage(ctx, creds)
		if err != nil {
			return errorUsageData(formatFetchError(err)), nil
		}
		if statusErr == nil {
			return data, nil
		}
		if statusErr.statusCode == http.StatusUnauthorized {
			if attempt > 0 {
				return errorUsageData("Authentication failed. Will retry."), nil
			}
			if refreshErr := c.refreshCredentials(ctx, creds, refreshModeForced); refreshErr != nil {
				if errors.Is(refreshErr, ErrInvalidGrant) {
					return reauthUsageData("AUTH_EXPIRED"), nil
				}
				return errorUsageData("Authentication failed. Will retry."), nil
			}
			creds, err = c.currentCredentials(ctx)
			if err != nil {
				return reauthUsageData(formatCredentialError(err)), err
			}
			continue
		}
		return errorUsageData(statusErr.message()), nil
	}
	return errorUsageData("Authentication failed. Will retry."), nil
}

func (c *Client) currentCredentials(ctx context.Context) (*credentials, error) {
	c.stateMu.Lock()
	loaded := c.loaded
	c.stateMu.Unlock()
	if loaded != nil {
		return loaded, nil
	}
	return c.loadCredentials(ctx)
}

func (c *Client) ensureFreshCredentials(ctx context.Context, creds *credentials) (*credentials, error) {
	currentInfo, err := os.Stat(creds.path)
	if err != nil {
		return nil, credentialsError{path: creds.path, err: fmt.Errorf("stat: %w", err)}
	}
	decision := credstore.ShouldRefresh(time.Now().UTC(), credstore.RefreshState{
		Snapshot:       creds.snapshot,
		CurrentModTime: currentInfo.ModTime(),
		ExpiresAt:      creds.expiresAt.Add(-expirySkew),
	})
	switch decision {
	case credstore.RefreshDecisionUseSnapshot:
		return creds, nil
	case credstore.RefreshDecisionReload:
		return c.loadCredentials(ctx)
	case credstore.RefreshDecisionRefresh:
		if err := c.refreshCredentials(ctx, creds, refreshModeProactive); err != nil {
			return nil, err
		}
		return c.currentCredentials(ctx)
	default:
		return nil, fmt.Errorf("unknown refresh decision %d", decision)
	}
}

func reauthUsageData(message string) usage.UsageData {
	command := reauthCommandText
	return usage.UsageData{ProviderName: providerName, PrimaryLabel: primaryLabel, SecondaryLabel: secondaryLabel, ShowSecondary: true, ReauthCommand: &command, Error: &message, NeedsReauth: true}
}

func errorUsageData(message string) usage.UsageData {
	return usage.UsageData{ProviderName: providerName, PrimaryLabel: primaryLabel, SecondaryLabel: secondaryLabel, ShowSecondary: true, Error: &message}
}
