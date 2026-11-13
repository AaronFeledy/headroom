package opencodego

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

const (
	providerName          = "OpenCodeGo"
	defaultUsageURL       = "https://opencode.ai/zen/go/v1/usage"
	signInCommandText     = "opencode auth login"
	userAgent             = "ClaudeUsageWidget"
	defaultHTTPTimeout    = 10 * time.Second
	maxUsageResponseBytes = 256 * 1024
)

// Options configures the OpenCode Go provider. Credentials are read-only: the
// provider never refreshes, rewrites, or rotates the OpenCode CLI login.
type Options struct {
	CredentialsPath string
	Environment     winprofile.Environment
	UsageURL        string
	HTTPClient      *http.Client
	Now             func() time.Time
}

type Provider struct {
	configured  string
	environment winprofile.Environment
	httpClient  *http.Client
	usageURL    string
	now         func() time.Time
}

func NewProvider(opts Options) *Provider {
	environment := opts.Environment
	if environment.GOOS == "" {
		environment.GOOS = runtime.GOOS
	}
	if environment.Env == nil {
		environment.Env = os.Getenv
	}
	if environment.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			environment.Home = home
		}
	}
	usageURL := opts.UsageURL
	if usageURL == "" {
		usageURL = defaultUsageURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout, CheckRedirect: keepCredentialHost}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Provider{
		configured:  opts.CredentialsPath,
		environment: environment,
		httpClient:  client,
		usageURL:    usageURL,
		now:         now,
	}
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
	data := usage.UsageData{
		ProviderName:   providerName,
		PrimaryLabel:   "5-Hour",
		SecondaryLabel: "Weekly",
		ReauthCommand:  strPtr(signInCommandText),
	}
	loaded := loadCredentials(ctx, p.configured, p.environment, p.now())
	source = loaded.source
	creds, err := loaded.credential, loaded.err
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return data, err
		}
		return credentialError(data, err), nil
	}
	if !creds.expiresAt.IsZero() && !p.now().Before(creds.expiresAt) {
		return credentialError(data, errCredentialExpired), nil
	}
	state = "signed_in"
	// Clients keep last-good meters through a failed poll only while this
	// identity matches, so it is set before the request can fail.
	data.CredentialEpoch = usage.CredentialEpoch(providerName, source.Name, creds.token)
	return p.fetchUsage(ctx, data, creds.token)
}

func (p *Provider) fetchUsage(ctx context.Context, data usage.UsageData, token string) (usage.UsageData, error) {
	response, err := p.send(ctx, token)
	if err != nil {
		return data, usage.TransportFailure(err)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		if err := drainAndClose(response); err != nil {
			return data, err
		}
		return reauth(data, "OpenCode Go auth expired. Run `opencode auth login` again."), nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data.FetchFailure = usage.HTTPFailure(response)
		message := fmt.Sprintf("OpenCode Go usage request failed (%d). Try again later.", response.StatusCode)
		if err := drainAndClose(response); err != nil {
			return data, err
		}
		data.Error = &message
		return data, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxUsageResponseBytes+1))
	closeErr := drainAndClose(response)
	if readErr != nil {
		return data, usage.TransportFailure(readErr)
	}
	if closeErr != nil {
		return data, closeErr
	}
	if len(body) > maxUsageResponseBytes {
		return responseChanged(data), nil
	}
	buckets, err := decodeUsage(body, p.now())
	if err != nil {
		return responseChanged(data), nil
	}
	return data.WithBuckets(buckets), nil
}

func (p *Provider) send(ctx context.Context, token string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create OpenCode Go request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := p.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send OpenCode Go request: %w", err)
	}
	return response, nil
}

// keepCredentialHost never follows a redirect away from the usage origin, so a
// bearer token cannot be carried to another host.
func keepCredentialHost(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

func credentialError(data usage.UsageData, err error) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	switch {
	case errors.Is(err, errCredentialExpired):
		return reauth(data, "OpenCode Go auth expired. Run `opencode auth login` again.")
	case errors.Is(err, errCredentialAmbiguous):
		return reauth(data, "OpenCode Go credentials are ambiguous. Run `opencode auth login` again.")
	case errors.Is(err, errCredentialInvalid):
		return reauth(data, "OpenCode Go credentials are invalid. Run `opencode auth login` again.")
	case errors.Is(err, errCredentialUnreadable):
		data.Error = strPtr("OpenCode Go credentials could not be read. Will retry.")
		return data
	default:
		data.Error = strPtr("OpenCode Go is not signed in. Run `opencode auth login`.")
		return data
	}
}

func reauth(data usage.UsageData, message string) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureAuth}
	data.Error = &message
	data.NeedsReauth = true
	return data
}

func responseChanged(data usage.UsageData) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	data.Error = strPtr("OpenCode Go usage response changed.")
	return data
}

func drainAndClose(response *http.Response) error {
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxUsageResponseBytes+1))
	closeErr := response.Body.Close()
	if copyErr == nil && closeErr == nil {
		return nil
	}
	return errors.Join(wrapIfErr("drain OpenCode Go response body", copyErr), wrapIfErr("close OpenCode Go response body", closeErr))
}

func wrapIfErr(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func strPtr(value string) *string { return &value }

var _ usage.Provider = (*Provider)(nil)
