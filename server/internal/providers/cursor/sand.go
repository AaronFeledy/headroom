package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	sandUsagePath   = "/api/dashboard/get-sand-usage-status"
	grokBotBucketID = "weekly_grok_bot"
)

func grokBotBucket(sand *cursorSandUsage) (usage.Bucket, bool) {
	if sand == nil {
		return usage.Bucket{}, false
	}
	if !sand.HasNonZeroIncludedLimit && sand.UsagePercent <= 0 {
		return usage.Bucket{}, false
	}
	reset := parseDate(&sand.NextResetTimestampUtc)
	return usage.Bucket{
		ID:          grokBotBucketID,
		Label:       "Grok Bot",
		Utilization: clampPercent(sand.UsagePercent),
		ResetsAt:    reset,
		StartsAt:    usage.ValidPeriodStart(parseDate(&sand.CurrentPeriodStart), reset),
	}, true
}

func (c *Client) fetchSandUsage(ctx context.Context, cookieHeader string) *cursorSandUsage {
	resp, err := c.postJSON(ctx, sandUsagePath, cookieHeader, []byte("{}"))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var sand cursorSandUsage
	if err := json.NewDecoder(resp.Body).Decode(&sand); err != nil {
		return nil
	}
	return &sand
}

func (c *Client) postJSON(ctx context.Context, path string, cookieHeader string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Cursor request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("Origin", c.requestOrigin())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Cursor request: %w", err)
	}
	return resp, nil
}

func (c *Client) requestOrigin() string {
	parsed, err := url.Parse(c.baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return defaultBaseURL
	}
	return parsed.Scheme + "://" + parsed.Host
}
