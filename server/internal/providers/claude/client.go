package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	providerName    = "Claude"
	primaryLabel    = "Current Session"
	secondaryLabel  = "Weekly"
	defaultUsageURL = "https://api.anthropic.com/api/oauth/usage"
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	betaHeader      = "oauth-2025-04-20"
	// Banked-reset discovery requires the Claude CLI identity (2.1.280+).
	// The legacy claude-code identity returns ineligible_reason: surface.
	userAgent         = "claude-cli/2.1.306 (external, cli)"
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
	APIBaseURL      string
	HTTPClient      *http.Client
	// Empty disables reset consumption; New never derives persistent state from HOME.
	ResetStateDir string
	// Name defaults to "Claude". Extra accounts use "Claude (<label>)".
	Name string
	// Account is nil for the default Claude row.
	Account *usage.Account
}

type Client struct {
	client        *http.Client
	usageURL      string
	tokenURL      string
	apiBaseURL    string
	name          string
	account       *usage.Account
	signInCommand *string
	paths         discoveryOptions
	goos          string

	stateMu            sync.Mutex
	refreshMu          sync.Mutex
	loaded             *credentials
	bankMu             sync.Mutex
	bank               bankCache
	resetMu            sync.Mutex
	resetStateDir      string
	resetJournalFailed atomic.Bool
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
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = "https://api.anthropic.com"
	}
	resetStateDir := opts.ResetStateDir
	if resetStateDir != "" {
		resetStateDir = filepath.Clean(resetStateDir)
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = providerName
	}
	account := opts.Account.Clone()
	// Banked-reset consumption is default-account-only: never give an extra
	// account a reset state directory even if a caller passes one.
	if account != nil {
		resetStateDir = ""
	}
	command := reauthCommandText
	signInCommand := &command
	if account != nil {
		signInCommand = nil
		credPath := expandHome(opts.CredentialsPath, opts.HomeDir)
		if filepath.Base(credPath) == ".credentials.json" {
			command = accountSignInCommand(runtime.GOOS, filepath.Dir(credPath))
			signInCommand = &command
		}
	}
	return &Client{
		client:        client,
		goos:          runtime.GOOS,
		usageURL:      usageURL,
		tokenURL:      tokenURL,
		apiBaseURL:    apiBaseURL,
		name:          name,
		account:       account,
		signInCommand: signInCommand,
		resetStateDir: resetStateDir,
		paths: discoveryOptions{
			configuredPath: opts.CredentialsPath,
			homeDir:        opts.HomeDir,
			wslHomeDir:     opts.WSLHomeDir,
			openCodePath:   opts.OpenCodePath,
		},
	}
}

func (c *Client) Name() string { return c.name }

func (c *Client) ProviderType() string { return providerName }

func (c *Client) Account() *usage.Account { return c.account.Clone() }

func (c *Client) Fetch(ctx context.Context) (result usage.UsageData, fetchErr error) {
	authExpired := false
	credentialsMissing := false
	var epoch *string
	var failure *usage.FetchFailure
	defer func() {
		result.CredentialEpoch = epoch
		result.FetchFailure = failure
		result.ProviderName = c.name
		result.ProviderType = providerName
		result.Account = c.account.Clone()
		c.stateMu.Lock()
		creds := c.loaded
		c.stateMu.Unlock()
		state := "signed_out"
		var source *usage.AuthSource
		if creds != nil {
			name := "Claude Code"
			if creds.source == credentialSourceOpenCode {
				name = "OpenCode"
			}
			if strings.HasPrefix(creds.path, `\\wsl.`) {
				name += " (WSL)"
			}
			source = &usage.AuthSource{Kind: "cli", Name: name}
			state = "signed_in"
			if result.NeedsReauth || authExpired {
				state = "expired"
			}
		}
		auth := usage.NewAuth(providerName, state, source)
		if c.account != nil && auth.SignInCommand != nil {
			auth.SignInCommand = c.signInCommand
		}
		if c.goos == "darwin" && credentialsMissing {
			auth.SignInCommand = nil
			result.ReauthCommand = nil
		}
		result.Auth = auth
	}()
	creds, err := c.currentCredentials(ctx)
	if err != nil {
		credentialsMissing = errors.Is(err, ErrCredentialsMissing)
		return c.reauthUsageData(formatCredentialError(err, c.goos)), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		ready, refreshErr := c.ensureFreshCredentials(ctx, creds)
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrInvalidGrant) {
				return c.reauthUsageData("AUTH_EXPIRED"), nil
			}
			return errorUsageData("Token refresh failed. Will retry."), nil
		}
		creds = ready
		epoch = usage.CredentialEpoch(c.name, creds.path, fmt.Sprint(creds.source), creds.accessToken)
		data, statusErr, err := c.fetchUsage(ctx, creds)
		if err != nil {
			var typed *usage.FetchFailure
			if errors.As(err, &typed) {
				failure = typed
			}
			return errorUsageData(formatFetchError(err)), nil
		}
		if statusErr == nil {
			authExpired = false
			c.attachResetCredits(ctx, creds, &data)
			failure = data.FetchFailure
			// A canceled bank attachment has no trustworthy reset binding.
			// Let the poller retain last-good display data as a failed fetch so
			// armed actions pause rather than treating missing binding as success.
			if err := ctx.Err(); err != nil {
				transportFailure := usage.TransportFailure(err)
				if failure == nil {
					failure = transportFailure
				}
				return data, transportFailure
			}
			return data, nil
		}
		failure = statusErr.failure
		if statusErr.statusCode == http.StatusUnauthorized {
			authExpired = true
			if attempt > 0 {
				return errorUsageData("Authentication failed. Will retry."), nil
			}
			if refreshErr := c.refreshCredentials(ctx, creds, refreshModeForced); refreshErr != nil {
				failure = nil
				if errors.Is(refreshErr, ErrInvalidGrant) {
					return c.reauthUsageData("AUTH_EXPIRED"), nil
				}
				return errorUsageData("Authentication failed. Will retry."), nil
			}
			creds, err = c.currentCredentials(ctx)
			if err != nil {
				credentialsMissing = errors.Is(err, ErrCredentialsMissing)
				return c.reauthUsageData(formatCredentialError(err, c.goos)), nil
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

func (c *Client) reauthUsageData(message string) usage.UsageData {
	return usage.UsageData{
		ProviderName:   c.name,
		PrimaryLabel:   primaryLabel,
		SecondaryLabel: secondaryLabel,
		ShowSecondary:  true,
		ReauthCommand:  c.signInCommand,
		Error:          &message,
		NeedsReauth:    true,
	}
}

func errorUsageData(message string) usage.UsageData {
	return usage.UsageData{ProviderName: providerName, PrimaryLabel: primaryLabel, SecondaryLabel: secondaryLabel, ShowSecondary: true, Error: &message}
}
