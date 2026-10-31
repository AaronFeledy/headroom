package grok

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func resetInventoryRequest(ctx context.Context, endpoint, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("Origin", "https://grok.com")
	return req, nil
}

func (p *Provider) readExtra(ctx context.Context, index int, creds credentials) (*usage.UsageDetails, time.Time) {
	var req *http.Request
	var err error
	if index == 3 {
		req, err = resetInventoryRequest(ctx, p.extraURLs[index], creds.accessToken)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, p.extraURLs[index], nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+creds.accessToken)
			req.Header.Set("X-XAI-Token-Auth", tokenAuthHeader)
			req.Header.Set("x-userid", creds.userID)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", userAgent)
		}
	}
	if err != nil {
		return nil, time.Time{}
	}
	client := *p.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxWebUsageResponseBytes+1))
	closeErr := resp.Body.Close()
	now := p.now()
	var retry time.Time
	if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			// Cap duration conversion, never wrap a long server cooldown into the past.
			seconds = min(seconds, int64((1<<63-1)/time.Second))
			retry = now.Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil {
			retry = date
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests && retry.IsZero() {
		retry = now.Add(5 * time.Minute)
	}
	if readErr != nil || closeErr != nil || len(body) > maxWebUsageResponseBytes || resp.StatusCode != http.StatusOK {
		return nil, retry
	}
	var details *usage.UsageDetails
	switch index {
	case 0:
		details, err = decodeMonetary(body, now)
	case 1:
		details, err = decodeTopUp(body, now)
	case 2:
		details, err = decodeUser(body, now)
	case 3:
		var inventory *usage.ResetInventoryDetails
		inventory, err = decodeResetInventory(body, resp.Header.Get("Grpc-Status"), now)
		if err == nil {
			details = &usage.UsageDetails{ResetInventory: inventory}
		}
	}
	if err != nil {
		return nil, retry
	}
	return details, retry
}
