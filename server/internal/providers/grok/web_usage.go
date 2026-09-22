package grok

//go:generate protoc --go_out=. --go_opt=paths=source_relative grok_credits.proto

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"google.golang.org/protobuf/proto"
)

const maxWebUsageResponseBytes = 1 << 20

func (p *Provider) SetCookieHeader(cookieHeader string) {
	p.webMu.Lock()
	defer p.webMu.Unlock()
	p.webCookie = strings.TrimSpace(cookieHeader)
}

func (p *Provider) populateWebUsage(ctx context.Context, data *usage.UsageData) (bool, error) {
	cookieHeader := p.webCookieHeader()
	if cookieHeader == "" {
		return false, nil
	}
	bucket, statusCode, grpcStatus, err := p.fetchWebUsage(ctx, cookieHeader)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || grpcStatus == "16" {
		p.clearWebCookie(cookieHeader)
		return false, nil
	}
	if bucket == nil {
		return false, nil
	}
	if data.Error != nil {
		replacement := baseUsageData().WithBuckets([]usage.Bucket{*bucket})
		*data = replacement
		return true, nil
	}
	buckets := append([]usage.Bucket{}, data.Buckets...)
	buckets = append(buckets, *bucket)
	*data = data.WithBuckets(buckets)
	data.SecondaryStatusText = nil
	return true, nil
}

func (p *Provider) fetchWebUsage(ctx context.Context, cookieHeader string) (*usage.Bucket, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.webBillingURL, bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	if err != nil {
		return nil, 0, "", fmt.Errorf("create Grok web usage request: %w", err)
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("Accept", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("X-User-Agent", "connect-es/2.1.1")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("send Grok web usage request: %w", err)
	}
	grpcStatus := resp.Header.Get("Grpc-Status")
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || (grpcStatus != "" && grpcStatus != "0") {
		if err := drainAndClose(resp); err != nil {
			return nil, resp.StatusCode, grpcStatus, err
		}
		return nil, resp.StatusCode, grpcStatus, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebUsageResponseBytes+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, resp.StatusCode, grpcStatus, fmt.Errorf("read Grok web usage response: %w", err)
	}
	if closeErr != nil {
		return nil, resp.StatusCode, grpcStatus, fmt.Errorf("close Grok web usage response: %w", closeErr)
	}
	if len(body) > maxWebUsageResponseBytes {
		return nil, resp.StatusCode, grpcStatus, fmt.Errorf("Grok web usage response exceeded size limit")
	}
	bucket, bodyStatus, err := parseWebUsageResponse(body)
	if err != nil {
		return nil, resp.StatusCode, grpcStatus, err
	}
	if bodyStatus != "" {
		grpcStatus = bodyStatus
	}
	if grpcStatus != "" && grpcStatus != "0" {
		return nil, resp.StatusCode, grpcStatus, nil
	}
	return bucket, resp.StatusCode, grpcStatus, nil
}

func parseWebUsageResponse(body []byte) (*usage.Bucket, string, error) {
	var bucket *usage.Bucket
	grpcStatus := ""
	for offset := 0; offset < len(body); {
		if len(body)-offset < 5 {
			return nil, grpcStatus, fmt.Errorf("truncated Grok web usage frame header")
		}
		flag := body[offset]
		frameLength := uint64(binary.BigEndian.Uint32(body[offset+1 : offset+5]))
		offset += 5
		if frameLength > uint64(len(body)-offset) {
			return nil, grpcStatus, fmt.Errorf("truncated Grok web usage frame")
		}
		payload := body[offset : offset+int(frameLength)]
		offset += int(frameLength)
		if flag&0x80 != 0 {
			grpcStatus = trailerStatus(payload)
			continue
		}
		if flag != 0 {
			return nil, grpcStatus, fmt.Errorf("unsupported Grok web usage frame flag %d", flag)
		}
		var response GetGrokCreditsConfigResponse
		if err := proto.Unmarshal(payload, &response); err != nil {
			return nil, grpcStatus, fmt.Errorf("decode Grok web usage response: %w", err)
		}
		bucket = weeklyBucket(response.GetConfig())
	}
	return bucket, grpcStatus, nil
}

func weeklyBucket(config *GrokCreditsConfig) *usage.Bucket {
	if config == nil {
		return nil
	}
	period := config.GetCurrentPeriod()
	if period == nil || period.GetType() != UsagePeriodType_USAGE_PERIOD_TYPE_WEEKLY {
		return nil
	}
	reset := timestampTime(period.GetEnd())
	if reset == nil {
		reset = timestampTime(config.GetBillingPeriodEnd())
	}
	return &usage.Bucket{
		ID:          usage.BucketWeekly,
		Label:       "Weekly",
		Utilization: math.Min(math.Max(float64(config.GetCreditUsagePercent()), 0), 100),
		ResetsAt:    reset,
		StartsAt:    usage.ValidPeriodStart(timestampTime(period.GetStart()), reset),
	}
}

func trailerStatus(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\r\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "grpc-status") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func timestampTime(value *Timestamp) *time.Time {
	if value == nil {
		return nil
	}
	parsed := time.Unix(value.GetSeconds(), int64(value.GetNanos())).UTC()
	return &parsed
}

func (p *Provider) webCookieHeader() string {
	p.webMu.RLock()
	defer p.webMu.RUnlock()
	return p.webCookie
}

func (p *Provider) clearWebCookie(cookieHeader string) {
	p.webMu.Lock()
	defer p.webMu.Unlock()
	if p.webCookie == cookieHeader {
		p.webCookie = ""
	}
}
