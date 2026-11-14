package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	providerName      = "Claude"
	primaryLabel      = "Current Session"
	secondaryLabel    = "Weekly"
	defaultUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	defaultTokenURL   = "https://platform.claude.com/v1/oauth/token"
	betaHeader        = "oauth-2025-04-20"
	oauthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultTimeout    = 15 * time.Second
	expirySkew        = 60 * time.Second
	reauthCommandText = "claude auth login"

	// Claude accepts roughly one usage request per two minutes per token, and
	// other clients spend the same budget, so live requests bound the cadence.
	minPollSpacing    = 3 * time.Minute
	maxPollSpacing    = 15 * time.Minute
	idlePollSpacing   = 10 * time.Minute
	paramRejectionTTL = 30 * time.Minute
)

type Options struct {
	CredentialsPath string
	ConfigDir       string
	Keychain        Keychain
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
	keychain      Keychain

	stateMu            sync.Mutex
	refreshMu          sync.Mutex
	loaded             *credentials
	bankMu             sync.Mutex
	bank               bankCache
	resetMu            sync.Mutex
	resetStateDir      string
	resetJournalFailed atomic.Bool
	emberMu            sync.Mutex
	emberToken         string
	emberUntil         time.Time
}

func (c *Client) Name() string { return c.name }

func (c *Client) ProviderType() string { return providerName }

// PollPolicy keeps Claude requests inside its per-token budget while adapting
// to observed rate limits and idle, unchanged readings.
func (c *Client) PollPolicy() usage.PollPolicy {
	return usage.PollPolicy{
		MinSpacing:  minPollSpacing,
		MaxSpacing:  maxPollSpacing,
		IdleSpacing: idlePollSpacing,
	}
}

func (c *Client) Account() *usage.Account { return c.account.Clone() }

func (c *Client) Fetch(ctx context.Context) (result usage.UsageData, fetchErr error) {
	authExpired := false
	commandless := false
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
		if commandless {
			auth.SignInCommand = nil
			result.ReauthCommand = nil
		}
		result.Auth = auth
	}()
	creds, err := c.currentCredentials(ctx)
	if err != nil {
		commandless = needsClaudeCode(err)
		return c.reauthUsageData(formatCredentialError(err, c.goos)), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		ready, refreshErr := c.ensureFreshCredentials(ctx, creds)
		if refreshErr != nil {
			commandless = needsClaudeCode(refreshErr)
			if isCredentialError(refreshErr) {
				return c.reauthUsageData(c.credentialErrorMessage(refreshErr)), nil
			}
			if errors.Is(refreshErr, ErrInvalidGrant) {
				return c.reauthUsageData("AUTH_EXPIRED"), nil
			}
			return errorUsageData("Token refresh failed. Will retry."), nil
		}
		creds = ready
		epoch = usage.CredentialEpoch(c.name, creds.path, fmt.Sprint(creds.source), creds.accessToken)
		data, bank, statusErr, err := c.fetchUsage(ctx, creds)
		if err != nil {
			var typed *usage.FetchFailure
			if errors.As(err, &typed) {
				failure = typed
			}
			return errorUsageData(formatFetchError(err)), nil
		}
		if statusErr == nil {
			authExpired = false
			c.attachResetCredits(ctx, creds, &data, bank)
			c.applyPlanSubtitle(ctx, creds, &data)
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
				commandless = needsClaudeCode(refreshErr)
				if isCredentialError(refreshErr) {
					return c.reauthUsageData(c.credentialErrorMessage(refreshErr)), nil
				}
				if errors.Is(refreshErr, ErrInvalidGrant) {
					return c.reauthUsageData("AUTH_EXPIRED"), nil
				}
				return errorUsageData("Authentication failed. Will retry."), nil
			}
			creds, err = c.currentCredentials(ctx)
			if err != nil {
				commandless = needsClaudeCode(err)
				return c.reauthUsageData(formatCredentialError(err, c.goos)), nil
			}
			continue
		}
		return errorUsageData(statusErr.message()), nil
	}
	return errorUsageData("Authentication failed. Will retry."), nil
}

func (c *Client) applyPlanSubtitle(ctx context.Context, creds *credentials, data *usage.UsageData) {
	if lockResetMutex(ctx, &c.bankMu) != nil {
		return
	}
	defer c.bankMu.Unlock()
	if c.bank.token != creds.accessToken || c.bank.planName == "" {
		return
	}
	plan := c.bank.planName
	data.Subtitle = &plan
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
