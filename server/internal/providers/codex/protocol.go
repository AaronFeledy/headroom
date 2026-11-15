package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func parseUsage(body []byte, data usage.UsageData) (usage.UsageData, error) {
	var decoded whamResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return data, fmt.Errorf("decode codex usage response: %w", err)
	}
	if decoded.PlanType != nil {
		data.Subtitle = decoded.PlanType
	}
	data.RateLimitResetCredits = parseResetCredits(decoded.RateLimitResetCredits)
	current, weekly := normalizeWindows(decoded.RateLimit.PrimaryWindow, decoded.RateLimit.SecondaryWindow)
	data.ProviderName = providerName
	// Some plans expose only a weekly window. Absence is not zero usage:
	// preserve real zero-valued windows, but never invent a missing limit.
	buckets := make([]usage.Bucket, 0, 2)
	if current != nil {
		value := usageBucket(current)
		buckets = append(buckets, usage.Bucket{ID: usage.BucketSession, Label: "5-Hour", Utilization: value.Utilization, ResetsAt: value.ResetsAt})
	}
	if weekly != nil {
		value := usageBucket(weekly)
		buckets = append(buckets, usage.Bucket{ID: usage.BucketWeekly, Label: "Weekly", Utilization: value.Utilization, ResetsAt: value.ResetsAt})
	}
	return data.WithBuckets(buckets), nil
}

const maxDesktopSafeInteger int64 = 9007199254740991

func parseResetCredits(raw json.RawMessage) *usage.RateLimitResetCredits {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var decoded struct {
		AvailableCount json.RawMessage `json:"available_count"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.AvailableCount) == 0 {
		return nil
	}
	var count *int64
	if err := json.Unmarshal(decoded.AvailableCount, &count); err != nil || count == nil || *count < 0 || *count > maxDesktopSafeInteger {
		return nil
	}
	return &usage.RateLimitResetCredits{AvailableCount: *count}
}

func baseUsage() usage.UsageData {
	reauth := "codex"
	return usage.UsageData{ProviderName: providerName, PrimaryLabel: "5-Hour", SecondaryLabel: "Weekly", ShowSecondary: true, ReauthCommand: &reauth}
}

func reauthUsage() usage.UsageData {
	data := baseUsage()
	message := "AUTH_EXPIRED"
	data.Error = &message
	data.NeedsReauth = true
	return data
}

func unwritableReauthUsage(cause error) usage.UsageData {
	data := baseUsage()
	message := "Sign-in expired, and Headroom can't save a refreshed one because " + credstore.NotWritablePhrase(cause) + ". Run `codex` to refresh it."
	data.Error = &message
	data.NeedsReauth = true
	return data
}

func usageBucket(window *whamWindow) usage.UsageBucket {
	if window == nil {
		return usage.UsageBucket{}
	}
	var resetsAt *time.Time
	if window.ResetAt != nil {
		parsed := time.Unix(*window.ResetAt, 0).UTC()
		resetsAt = &parsed
	}
	return usage.UsageBucket{Utilization: window.UsedPercent, ResetsAt: resetsAt}
}

func isInvalidGrantBody(body []byte) bool {
	var response struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err == nil {
		code := response.Code
		var nested struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(response.Error, &nested) == nil && nested.Code != "" {
			code = nested.Code
		} else {
			var direct string
			if json.Unmarshal(response.Error, &direct) == nil {
				code = direct
			}
		}
		return isTerminalRefreshCode(code)
	}
	return isTerminalRefreshCode(string(body))
}

func isTerminalRefreshCode(code string) bool {
	lower := strings.ToLower(code)
	return strings.Contains(lower, "refresh_token_expired") || strings.Contains(lower, "refresh_token_reused") || strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "refresh_token_invalidated")
}

func normalizeWindows(primary, secondary *whamWindow) (*whamWindow, *whamWindow) {
	if primary == nil {
		// A lone window keeps its reported position unless its duration says
		// otherwise, so an unlabelled secondary window stays weekly.
		if windowRoleOf(secondary) == windowRoleSession {
			return secondary, nil
		}
		return nil, secondary
	}
	if secondary == nil {
		if windowRoleOf(primary) == windowRoleWeekly {
			return nil, primary
		}
		return primary, nil
	}
	if windowRoleOf(primary) == windowRoleWeekly && windowRoleOf(secondary) != windowRoleWeekly {
		return secondary, primary
	}
	return primary, secondary
}

type windowRole uint8

const (
	windowRoleUnknown windowRole = iota
	windowRoleSession
	windowRoleWeekly
)

func windowRoleOf(window *whamWindow) windowRole {
	if window == nil || window.LimitWindowSeconds == nil {
		return windowRoleUnknown
	}
	switch *window.LimitWindowSeconds {
	case 18000:
		return windowRoleSession
	case 604800:
		return windowRoleWeekly
	default:
		return windowRoleUnknown
	}
}

type refreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    *int   `json:"expires_in"`
}

type whamResponse struct {
	PlanType              *string         `json:"plan_type"`
	RateLimitResetCredits json.RawMessage `json:"rate_limit_reset_credits"`
	RateLimit             struct {
		PrimaryWindow   *whamWindow `json:"primary_window"`
		SecondaryWindow *whamWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type whamWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	ResetAt            *int64  `json:"reset_at"`
	LimitWindowSeconds *int64  `json:"limit_window_seconds"`
}
