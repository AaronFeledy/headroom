package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// fetchUsage makes at most one parameterized usage request. The same body
// carries the ordinary windows and the cedar_ember reset-status block. Claude
// may reject the parameter outright; that rejection is remembered per access
// token so later polls use the plain URL until the window expires.
func (c *Client) fetchUsage(ctx context.Context, creds *credentials) (usage.UsageData, *bankStatus, *statusError, error) {
	endpoint, parameterized := c.usageEndpoint(creds.accessToken)
	data, status, statusErr, err := c.requestUsage(ctx, endpoint, creds)
	if err != nil || statusErr == nil || !parameterized || !parameterRejected(statusErr) {
		return data, status, statusErr, err
	}
	c.rememberParameterRejection(creds.accessToken)
	return c.requestUsage(ctx, c.usageURL, creds)
}

func (c *Client) requestUsage(ctx context.Context, endpoint string, creds *credentials) (usage.UsageData, *bankStatus, *statusError, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return usage.UsageData{}, nil, nil, fmt.Errorf("build usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.client.Do(req)
	if err != nil {
		return usage.UsageData{}, nil, nil, usage.TransportFailure(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		failure := usage.HTTPFailure(resp)
		return usage.UsageData{}, nil, &statusError{statusCode: resp.StatusCode, body: string(body), failure: failure}, nil
	}
	if err != nil {
		return usage.UsageData{}, nil, nil, usage.TransportFailure(err)
	}
	parsed, err := parseUsageResponse(body, creds.subscriptionType)
	if err != nil {
		return usage.UsageData{}, nil, nil, err
	}
	return parsed, parseBankStatus(body), nil, nil
}

func (c *Client) usageEndpoint(token string) (string, bool) {
	c.emberMu.Lock()
	defer c.emberMu.Unlock()
	if c.emberToken == token && time.Now().Before(c.emberUntil) {
		return c.usageURL, false
	}
	return withCedarEmberParam(c.usageURL), true
}

func (c *Client) rememberParameterRejection(token string) {
	c.emberMu.Lock()
	defer c.emberMu.Unlock()
	c.emberToken = token
	c.emberUntil = time.Now().Add(paramRejectionTTL)
}

func withCedarEmberParam(raw string) string {
	if strings.Contains(raw, "?") {
		return raw + "&cedar_ember=1"
	}
	return raw + "?cedar_ember=1"
}

func parameterRejected(statusErr *statusError) bool {
	if statusErr == nil {
		return false
	}
	if statusErr.statusCode == http.StatusBadRequest {
		return true
	}
	return statusErr.statusCode == http.StatusForbidden && !strings.Contains(statusErr.body, "user:profile")
}
