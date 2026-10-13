package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type usageResponse struct {
	FiveHour          *usageBucketResponse `json:"five_hour"`
	SevenDay          *usageBucketResponse `json:"seven_day"`
	SevenDayOAuthApps *usageBucketResponse `json:"seven_day_oauth_apps"`
	SevenDaySonnet    *usageBucketResponse `json:"seven_day_sonnet"`
	SevenDayOpus      *usageBucketResponse `json:"seven_day_opus"`
	Limits            []limitEntry         `json:"limits"`
	ExtraUsage        *extraUsageResponse  `json:"extra_usage"`
}

type usageBucketResponse struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type limitEntry struct {
	Kind     string     `json:"kind"`
	Percent  *float64   `json:"percent"`
	ResetsAt string     `json:"resets_at"`
	IsActive *bool      `json:"is_active"`
	Scope    limitScope `json:"scope"`
}

type limitScope struct {
	Model limitModel `json:"model"`
}

type limitModel struct {
	DisplayName string `json:"display_name"`
}

type statusError struct {
	failure    *usage.FetchFailure
	statusCode int
	body       string
}

func (e statusError) message() string {
	if e.statusCode == http.StatusTooManyRequests {
		return "Rate limited. Will retry."
	}
	return fmt.Sprintf("API error (%d): %s", e.statusCode, e.body)
}

func (c *Client) fetchUsage(ctx context.Context, creds *credentials) (usage.UsageData, *statusError, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageURL, nil)
	if err != nil {
		return usage.UsageData{}, nil, fmt.Errorf("build usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return usage.UsageData{}, nil, usage.TransportFailure(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return usage.UsageData{}, &statusError{statusCode: resp.StatusCode, body: string(body), failure: usage.HTTPFailure(resp)}, nil
	}
	if err != nil {
		return usage.UsageData{}, nil, usage.TransportFailure(err)
	}
	parsed, err := parseUsageResponse(body, creds.subscriptionType)
	if err != nil {
		return usage.UsageData{}, nil, err
	}
	return parsed, nil, nil
}

func parseUsageResponse(body []byte, subscription *string) (usage.UsageData, error) {
	var response usageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return usage.UsageData{}, fmt.Errorf("parse usage response: %w", err)
	}

	var session usage.Bucket
	var weekly usage.Bucket
	sessionFound := false
	weeklyFound := false
	sessionActive := false
	weeklyActive := false
	scoped := make([]usage.Bucket, 0, len(response.Limits))
	for index, limit := range response.Limits {
		active := limit.IsActive == nil || *limit.IsActive
		window := usageBucketResponse{Utilization: limit.Percent, ResetsAt: limit.ResetsAt}
		switch limit.Kind {
		case "session":
			if limit.Percent == nil || sessionFound && (sessionActive || !active) {
				continue
			}
			bucket, err := window.toBucket("session", primaryLabel)
			if err != nil {
				return usage.UsageData{}, fmt.Errorf("limits[%d] session: %w", index, err)
			}
			session = bucket
			sessionFound = true
			sessionActive = active
		case "weekly_all":
			if limit.Percent == nil || weeklyFound && (weeklyActive || !active) {
				continue
			}
			bucket, err := window.toBucket("weekly", secondaryLabel)
			if err != nil {
				return usage.UsageData{}, fmt.Errorf("limits[%d] weekly_all: %w", index, err)
			}
			weekly = bucket
			weeklyFound = true
			weeklyActive = active
		case "weekly_scoped":
			if strings.TrimSpace(limit.Scope.Model.DisplayName) == "" || limit.Percent == nil {
				continue
			}
			bucket, err := window.toBucket("weekly_"+slugify(limit.Scope.Model.DisplayName), limit.Scope.Model.DisplayName)
			if err != nil {
				return usage.UsageData{}, fmt.Errorf("limits[%d] weekly_scoped: %w", index, err)
			}
			scoped = append(scoped, bucket)
		default:
			continue
		}
	}

	if !sessionFound && response.FiveHour != nil && response.FiveHour.Utilization != nil {
		bucket, err := response.FiveHour.toBucket("session", primaryLabel)
		if err != nil {
			return usage.UsageData{}, fmt.Errorf("five_hour: %w", err)
		}
		session = bucket
		sessionFound = true
	}
	// Backward compat: promote the first weekly-family window into the session
	// slot when no session/five_hour window exists. Track it to avoid emitting a
	// duplicate model bucket below.
	var sessionFallbackWindow *usageBucketResponse
	if !sessionFound {
		fallbacks := []*usageBucketResponse{
			response.SevenDay,
			response.SevenDayOAuthApps,
			response.SevenDaySonnet,
			response.SevenDayOpus,
		}
		for _, window := range fallbacks {
			if window == nil || window.Utilization == nil {
				continue
			}
			bucket, err := window.toBucket("session", secondaryLabel)
			if err != nil {
				return usage.UsageData{}, fmt.Errorf("session fallback: %w", err)
			}
			session = bucket
			sessionFound = true
			sessionFallbackWindow = window
			break
		}
	}
	if !sessionFound {
		return usage.UsageData{}, fmt.Errorf("missing session data")
	}
	if !weeklyFound && response.SevenDay != nil && response.SevenDay.Utilization != nil {
		bucket, err := response.SevenDay.toBucket("weekly", secondaryLabel)
		if err != nil {
			return usage.UsageData{}, fmt.Errorf("seven_day: %w", err)
		}
		weekly = bucket
		weeklyFound = true
	}

	buckets := make([]usage.Bucket, 0, 2+len(scoped)+2)
	buckets = append(buckets, session)
	if weeklyFound {
		buckets = append(buckets, weekly)
	}
	buckets = append(buckets, scoped...)
	legacy := []struct {
		id     string
		label  string
		window *usageBucketResponse
	}{
		{id: "weekly_sonnet", label: "Sonnet", window: response.SevenDaySonnet},
		{id: "weekly_opus", label: "Opus", window: response.SevenDayOpus},
	}
	for _, candidate := range legacy {
		if candidate.window == nil || candidate.window.Utilization == nil || candidate.window == sessionFallbackWindow || containsBucket(buckets, candidate.id) {
			continue
		}
		bucket, err := candidate.window.toBucket(candidate.id, candidate.label)
		if err != nil {
			return usage.UsageData{}, fmt.Errorf("%s: %w", candidate.id, err)
		}
		buckets = append(buckets, bucket)
	}

	if extra, ok := response.ExtraUsage.toBucket(); ok {
		buckets = append(buckets, extra)
	}

	base := usage.UsageData{ProviderName: providerName, Subtitle: subscription}
	return base.WithBuckets(buckets), nil
}

func containsBucket(buckets []usage.Bucket, id string) bool {
	for _, bucket := range buckets {
		if bucket.ID == id {
			return true
		}
	}
	return false
}

func slugify(value string) string {
	var slug strings.Builder
	separatorPending := false
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separatorPending && slug.Len() > 0 {
				slug.WriteByte('_')
			}
			slug.WriteRune(character)
			separatorPending = false
			continue
		}
		separatorPending = slug.Len() > 0
	}
	if slug.Len() == 0 {
		return "model"
	}
	return slug.String()
}

func (b usageBucketResponse) toBucket(id string, label string) (usage.Bucket, error) {
	bucket, err := b.toUsageBucket()
	if err != nil {
		return usage.Bucket{}, err
	}
	return usage.Bucket{ID: id, Label: label, Utilization: bucket.Utilization, ResetsAt: bucket.ResetsAt}, nil
}

func (b usageBucketResponse) toUsageBucket() (usage.UsageBucket, error) {
	var reset *time.Time
	if strings.TrimSpace(b.ResetsAt) != "" {
		parsed, err := time.Parse(time.RFC3339, b.ResetsAt)
		if err != nil {
			return usage.UsageBucket{}, fmt.Errorf("resets_at: %w", err)
		}
		utc := parsed.UTC()
		reset = &utc
	}
	return usage.UsageBucket{Utilization: math.Min(math.Max(*b.Utilization, 0), 100), ResetsAt: reset}, nil
}

func formatCredentialError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), ErrCredentialsMissing.Error()) {
		return "Credentials not found. Run claude to authenticate."
	}
	return "Credentials are invalid. Run claude to authenticate."
}

func formatFetchError(err error) string {
	if err == nil {
		return ""
	}
	if usage.IsTimeout(err) {
		return "Request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "Request canceled"
	}
	return "Usage request failed. Will retry."
}
