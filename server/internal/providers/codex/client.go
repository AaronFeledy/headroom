package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type Client struct {
	store              *CredentialStore
	http               *http.Client
	usageURL           string
	tokenURL           string
	refreshLog         credstore.RefreshLog
	refreshMu          sync.Mutex
	resetMu            sync.Mutex
	resetAttemptID     string
	resetAccountID     string
	resetStartedAt     time.Time
	resetUsageObserved bool
	resetAttempts      map[string]resetAttempt
}

func New(opts Options) *Client {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	usageURL := opts.UsageURL
	if usageURL == "" {
		usageURL = defaultUsageURL
	}
	tokenURL := opts.TokenURL
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}
	discovery := opts.Discovery
	if opts.CredentialsPath != "" {
		discovery.ConfiguredPath = opts.CredentialsPath
	}
	provider := &Client{
		store:      NewCredentialStore(CredentialOptions{Path: opts.CredentialsPath, Discovery: discovery}),
		http:       client,
		usageURL:   usageURL,
		tokenURL:   tokenURL,
		refreshLog: credstore.RefreshLog{Logger: opts.Logger},
	}
	provider.store.warn = provider.refreshLog.SavedWithError
	return provider
}

func (c *Client) Name() string { return providerName }

func (c *Client) Fetch(ctx context.Context) (result usage.UsageData, fetchErr error) {
	var epoch *string
	defer func() {
		result.CredentialEpoch = epoch
		c.store.mu.RLock()
		loaded, sourceKind, path := c.store.loaded, c.store.source, c.store.path
		c.store.mu.RUnlock()
		state := "signed_out"
		var source *usage.AuthSource
		if loaded {
			name := "Codex"
			if sourceKind == CredentialSourceOpenCode {
				name = "OpenCode"
			}
			if strings.HasPrefix(path, `\\wsl.`) {
				name += " (WSL)"
			}
			source = &usage.AuthSource{Kind: "cli", Name: name}
			state = "signed_in"
			if result.NeedsReauth {
				state = "expired"
			}
		}
		result.Auth = usage.NewAuth(providerName, state, source)
	}()
	fetchStartedAt := time.Now()
	data := baseUsage()
	if err := ctx.Err(); err != nil {
		return data, err
	}
	creds, err := c.store.Current(ctx)
	if err != nil {
		if errors.Is(err, ErrCredentialsMissing) {
			data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
			message := "Run `codex` to sign in."
			data.Error = &message
			return data, nil
		}
		return data, err
	}
	if c.store.NeedsRefresh(time.Now().UTC()) {
		result, refreshErr := c.refresh(ctx, creds)
		if refreshErr != nil && !errors.Is(refreshErr, credstore.ErrNotWritable) {
			return data, refreshErr
		}
		// An unwritable store leaves the current token usable until rejected.
		if refreshErr == nil {
			if result == usage.RefreshInvalidGrant {
				return reauthUsage(), nil
			}
			if result == usage.RefreshFailed {
				message := "Token refresh failed. Will retry."
				data.Error = &message
				return data, nil
			}
			creds, err = c.store.Load(ctx)
			if err != nil {
				return data, err
			}
		}
	}
	epoch = usage.CredentialEpoch(providerName, creds.Path, fmt.Sprint(creds.Source), creds.AccessToken, creds.AccountID)
	resp, err := c.sendUsage(ctx, creds)
	if err != nil {
		return data, usage.TransportFailure(err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if err := drainAndCloseResponseBody(resp, "close codex unauthorized response body"); err != nil {
			return data, err
		}
		// The rejected pair goes through RefreshTokens: a pair another writer
		// already replaced is adopted without an exchange.
		result, refreshErr := c.refresh(ctx, creds)
		if refreshErr != nil {
			if errors.Is(refreshErr, credstore.ErrNotWritable) {
				return unwritableReauthUsage(refreshErr), nil
			}
			return data, refreshErr
		}
		if result == usage.RefreshInvalidGrant {
			return reauthUsage(), nil
		}
		if result != usage.RefreshSuccess {
			message := "Token refresh failed. Will retry."
			data.Error = &message
			return data, nil
		}
		creds, err = c.store.Load(ctx)
		if err != nil {
			return data, err
		}
		epoch = usage.CredentialEpoch(providerName, creds.Path, fmt.Sprint(creds.Source), creds.AccessToken, creds.AccountID)
		resp, err = c.sendUsage(ctx, creds)
		if err != nil {
			return data, usage.TransportFailure(err)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data.FetchFailure = usage.HTTPFailure(resp)
		message := fmt.Sprintf("API error (%d)", resp.StatusCode)
		data.Error = &message
		if err := drainAndCloseResponseBody(resp, "close codex error response body"); err != nil {
			return data, err
		}
		return data, nil
	}
	body, err := io.ReadAll(resp.Body)
	closeErr := closeResponseBody(resp, "close codex usage response body")
	if err != nil {
		return data, usage.TransportFailure(err)
	}
	if closeErr != nil {
		return data, closeErr
	}
	parsed, err := parseUsage(body, data)
	if err != nil {
		return data, err
	}
	parsed.ProviderAccountID = creds.AccountID
	if parsed.RateLimitResetCredits != nil {
		parsed.RateLimitResetCredits.AccountFingerprint = accountFingerprint(creds.AccountID)
	}
	c.observeResetUsage(parsed, fetchStartedAt)
	return parsed, nil
}

func (c *Client) sendUsage(ctx context.Context, creds Credentials) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build codex usage request: %w", err)
	}
	applyUsageHeaders(req, creds)
	return c.http.Do(req)
}

func drainAndCloseResponseBody(resp *http.Response, operation string) error {
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return errors.Join(fmt.Errorf("drain %s: %w", operation, err), fmt.Errorf("%s: %w", operation, closeErr))
		}
		return fmt.Errorf("drain %s: %w", operation, err)
	}
	return closeResponseBody(resp, operation)
}

func closeResponseBody(resp *http.Response, operation string) error {
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
