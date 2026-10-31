package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	sandUsagePath   = "/api/dashboard/get-sand-usage-status"
	grokBotBucketID = "weekly_grok_bot"
)

func grokBotBucket(sand *cursorSandUsage) (usage.Bucket, bool) {
	if sand == nil || sand.stale {
		return usage.Bucket{}, false
	}
	if sand.UsesPooledEnterpriseAllowance != nil && *sand.UsesPooledEnterpriseAllowance {
		return usage.Bucket{}, false
	}
	if !sand.HasNonZeroIncludedLimit && sand.UsagePercent <= 0 {
		return usage.Bucket{}, false
	}
	measurement := usage.MeasurementPercent
	if !sand.percentReported && sand.UsagePercent == 0 {
		measurement = usage.MeasurementStatus
	}
	reset := parseDate(&sand.NextResetTimestampUtc)
	return usage.Bucket{
		ID:          grokBotBucketID,
		Label:       "Grok Bot",
		Utilization: clampPercent(sand.UsagePercent),
		Measurement: measurement,
		ResetsAt:    reset,
		StartsAt:    usage.ValidPeriodStart(parseDate(&sand.CurrentPeriodStart), reset),
	}, true
}

func (c *Client) fetchSandUsage(ctx context.Context, cookieHeader string) *cursorSandUsage {
	entry := c.optionalRead(ctx, cookieHeader, sandUsagePath)
	var sand cursorSandUsage
	if err := json.Unmarshal(entry.body, &sand); err != nil {
		return nil
	}
	sand.observedAt = &entry.observed
	sand.stale = c.discovery.Now().Sub(entry.observed) >= 5*time.Minute
	return &sand
}

func (s *cursorSandUsage) UnmarshalJSON(body []byte) error {
	type sandAlias cursorSandUsage
	var parsed sandAlias
	wire := struct {
		*sandAlias
		Percent *float64 `json:"usagePercent"`
	}{sandAlias: &parsed}
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	if wire.Percent != nil {
		if !validPercent(wire.Percent) {
			return errOptionalBody
		}
		parsed.UsagePercent = *wire.Percent
		parsed.percentReported = true
	}
	*s = cursorSandUsage(parsed)
	return nil
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
