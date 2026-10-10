package grok

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	providerName       = "Grok"
	billingURL         = "https://cli-chat-proxy.grok.com/v1/billing"
	settingsURL        = "https://cli-chat-proxy.grok.com/v1/settings"
	webBillingURL      = "https://grok.com/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig"
	tokenURL           = "https://auth.x.ai/oauth2/token"
	oauthClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	tokenAuthHeader    = "xai-grok-cli"
	userAgent          = "ClaudeUsageWidget"
	defaultHTTPTimeout = 10 * time.Second
	refreshBuffer      = 5 * time.Minute
)

var (
	errInvalidGrant     = errors.New("grok: invalid grant")
	errMissingAuthEntry = errors.New("grok: missing auth.x.ai entry")
	errRefreshFailed    = errors.New("grok: refresh failed")
)

type Options struct {
	CredentialsPath string
	HTTPClient      *http.Client
	BillingURL      string
	SettingsURL     string
	WebBillingURL   string
	TokenURL        string
	Now             func() time.Time
}

type Provider struct {
	credentialsPath string
	httpClient      *http.Client
	billingURL      string
	settingsURL     string
	webBillingURL   string
	tokenURL        string
	now             func() time.Time
	store           credentialStore
	webMu           sync.RWMutex
	webCookie       string
}

func NewProvider(opts Options) (*Provider, error) {
	path, err := resolveCredentialsPath(opts.CredentialsPath)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	provider := &Provider{
		credentialsPath: path,
		httpClient:      client,
		billingURL:      defaultString(opts.BillingURL, billingURL),
		settingsURL:     defaultString(opts.SettingsURL, settingsURL),
		webBillingURL:   defaultString(opts.WebBillingURL, webBillingURL),
		tokenURL:        defaultString(opts.TokenURL, tokenURL),
		now:             opts.Now,
	}
	if provider.now == nil {
		provider.now = func() time.Time { return time.Now().UTC() }
	}
	return provider, nil
}

func (p *Provider) Name() string { return providerName }

func (p *Provider) Fetch(ctx context.Context) (result usage.UsageData, fetchErr error) {
	state := "signed_out"
	var source *usage.AuthSource
	defer func() {
		if result.NeedsReauth && source != nil {
			state = "expired"
		}
		result.Auth = usage.NewAuth(providerName, state, source)
	}()
	data := baseUsageData()
	creds, err := p.credentials(ctx)
	if err != nil {
		data, err = p.authError(data, err)
	} else {
		ready := true
		state = "signed_in"
		name := "Grok CLI"
		if strings.HasPrefix(p.credentialsPath, `\\wsl.`) {
			name += " (WSL)"
		}
		source = &usage.AuthSource{Kind: "cli", Name: name}
		if creds.needsRefresh(p.now()) {
			creds, err = p.refresh(ctx, creds, refreshReasonProactive)
			if err != nil {
				ready = false
				data, err = p.refreshError(data, err)
			}
		}
		if ready && err == nil {
			data, err = p.fetchWithToken(ctx, data, creds)
			var failure *usage.FetchFailure
			if errors.As(err, &failure) {
				data.FetchFailure = failure
			}
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return data, err
	}
	for _, bucket := range data.Buckets {
		if bucket.ID == usage.BucketWeekly {
			return data, err
		}
	}
	webAdded, webErr := p.populateWebUsage(ctx, &data)
	if webErr != nil {
		if ctx.Err() == nil && err == nil && data.Error == nil && len(data.Buckets) > 0 {
			return data, nil
		}
		return data, webErr
	}
	if webAdded {
		if source == nil {
			state = "signed_in"
			source = &usage.AuthSource{Kind: "desktop", Name: "browser"}
		}
		return data, nil
	}
	return data, err
}

func (p *Provider) fetchWithToken(ctx context.Context, data usage.UsageData, creds credentials) (usage.UsageData, error) {
	parsedURL, err := url.Parse(p.billingURL)
	if err != nil {
		return data, fmt.Errorf("parse Grok billing URL: %w", err)
	}
	query := parsedURL.Query()
	query.Set("format", "credits")
	parsedURL.RawQuery = query.Encode()
	requestURL := parsedURL.String()
	data.CredentialEpoch = usage.CredentialEpoch(providerName, p.credentialsPath, creds.entryKey, creds.accessToken, creds.userID)
	billingResp, err := p.sendGet(ctx, requestURL, creds)
	if err != nil {
		return data, usage.TransportFailure(err)
	}

	if billingResp.StatusCode == http.StatusUnauthorized || billingResp.StatusCode == http.StatusForbidden {
		if err := drainAndClose(billingResp); err != nil {
			return data, err
		}
		refreshed, err := p.refresh(ctx, creds, refreshReasonUnauthorized)
		if err != nil {
			return p.refreshError(data, err)
		}
		creds = refreshed
		data.CredentialEpoch = usage.CredentialEpoch(providerName, p.credentialsPath, creds.entryKey, creds.accessToken, creds.userID)
		billingResp, err = p.sendGet(ctx, requestURL, creds)
		if err != nil {
			return data, usage.TransportFailure(err)
		}
	}

	if billingResp.StatusCode == http.StatusUnauthorized || billingResp.StatusCode == http.StatusForbidden {
		if err := drainAndClose(billingResp); err != nil {
			return data, err
		}
		return reauth(data), nil
	}
	if billingResp.StatusCode < http.StatusOK || billingResp.StatusCode >= http.StatusMultipleChoices {
		data.FetchFailure = usage.HTTPFailure(billingResp)
		if err := drainAndClose(billingResp); err != nil {
			return data, err
		}
		data.Error = strPtr(fmt.Sprintf("Grok billing request failed (%d). Try again later.", billingResp.StatusCode))
		return data, nil
	}
	body, readErr := io.ReadAll(billingResp.Body)
	if readErr != nil {
		data.FetchFailure = usage.TransportFailure(readErr)
		if closeErr := drainAndClose(billingResp); closeErr != nil {
			return data, closeErr
		}
		return data, data.FetchFailure
	}
	if err := mapBilling(bytes.NewReader(body), &data, p.now()); err != nil {
		data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
		if closeErr := drainAndClose(billingResp); closeErr != nil {
			return data, closeErr
		}
		data.Error = strPtr("Grok billing response changed.")
		return data, nil
	}
	if err := drainAndClose(billingResp); err != nil {
		return data, err
	}
	p.populateSettings(ctx, creds, &data)
	return data, nil
}

func (p *Provider) authError(data usage.UsageData, err error) (usage.UsageData, error) {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return data, err
	}
	if errors.Is(err, errMissingAuthEntry) {
		return reauth(data), nil
	}
	data.Error = strPtr("Grok auth error. Run `grok login`.")
	return data, nil
}

func (p *Provider) refreshError(data usage.UsageData, err error) (usage.UsageData, error) {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return data, err
	}
	if errors.Is(err, errInvalidGrant) {
		return reauth(data), nil
	}
	data.Error = strPtr("Token refresh failed. Will retry.")
	return data, nil
}

func baseUsageData() usage.UsageData {
	return usage.UsageData{
		ProviderName:   providerName,
		PrimaryLabel:   "Credits",
		SecondaryLabel: "Pay as you go",
		ShowSecondary:  false,
		ReauthCommand:  strPtr("grok login"),
	}
}

func reauth(data usage.UsageData) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureAuth}
	data.Error = strPtr("Grok auth expired. Run `grok login` again.")
	data.NeedsReauth = true
	return data
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func strPtr(value string) *string { return &value }

var _ usage.Provider = (*Provider)(nil)
