package cursor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const grantListPath = "/aiserver.v1.DashboardService/GetClientVisibleCreditGrants"
const grantBalancePath = "/aiserver.v1.DashboardService/GetCreditGrantsBalance"

type grantWire struct {
	DisplayName string      `json:"displayName"`
	Remaining   exactMinor  `json:"remainingCents"`
	Total       exactMinor  `json:"totalCents"`
	Expires     epochMillis `json:"expiresAtMs"`
}

type grantResponse struct {
	Grants          *[]grantWire `json:"grants"`
	HasCreditGrants *bool        `json:"hasCreditGrants"`
	Balance         exactMinor   `json:"creditBalanceCents"`
}

type grantObservation struct {
	details usage.CreditGrantDetails
}

func selectedJWT(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name != "WorkosCursorSessionToken" {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			return ""
		}
		if _, token, found := strings.Cut(decoded, "::"); found {
			decoded = token
		}
		if _, err := decodeJWTClaims(decoded); err == nil {
			return decoded
		}
	}
	return ""
}

func (c *Client) grantRequest(ctx context.Context, cookie, path string) (*http.Response, error) {
	token := selectedJWT(cookie)
	if token == "" {
		return nil, ErrInvalidToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcBaseURL+path, strings.NewReader("{}"))
	if err != nil {
		return nil, errOptionalBody
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+token)
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

func parseGrants(body []byte) *grantObservation {
	var wire grantResponse
	if json.Unmarshal(body, &wire) != nil || wire.Balance.invalid {
		return nil
	}
	result := &grantObservation{details: usage.CreditGrantDetails{Observation: usage.Observation{State: usage.DetailOK}}}
	if wire.Grants != nil {
		if wire.HasCreditGrants != nil && !*wire.HasCreditGrants && len(*wire.Grants) > 0 {
			return nil
		}
		if len(*wire.Grants) > usage.MaxDetailItems {
			return nil
		}
		result.details.Items = make([]usage.CreditGrant, 0, len(*wire.Grants))
		for _, row := range *wire.Grants {
			if row.Remaining.invalid || row.Total.invalid || row.Expires.invalid || (row.Remaining.value == nil && row.Total.value == nil) {
				return nil
			}
			if row.Remaining.value != nil && row.Total.value != nil && *row.Remaining.value > *row.Total.value {
				return nil
			}
			name := "Credit grant"
			if safe := safeLabel(row.DisplayName); safe != nil {
				name = *safe
			}
			result.details.Items = append(result.details.Items, usage.CreditGrant{DisplayName: name, Currency: "USD", RemainingMinor: row.Remaining.value, TotalMinor: row.Total.value, ExpiresAt: row.Expires.value, Scope: &usage.UsageScope{Kind: "unknown"}})
		}
		return result
	}
	if wire.HasCreditGrants != nil && !*wire.HasCreditGrants {
		if wire.Balance.value != nil && *wire.Balance.value > 0 {
			return nil
		}
		result.details.Items = []usage.CreditGrant{}
		return result
	}
	if wire.Balance.value != nil {
		return result
	}
	return nil
}

func (c *Client) fetchGrants(ctx context.Context, cookie string) *grantObservation {
	if selectedJWT(cookie) == "" {
		return nil
	}
	entry := c.optionalRead(ctx, cookie, grantListPath)
	result := parseGrants(entry.body)
	if result == nil {
		entry = c.optionalRead(ctx, cookie, grantBalancePath)
		result = parseGrants(entry.body)
	}
	if result != nil {
		result.details.Observation = usage.Observation{State: usage.DetailOK, ObservedAt: &entry.observed}
		if c.discovery.Now().Sub(entry.observed) >= 5*time.Minute {
			result.details.State = usage.DetailStale
		}
		if result.details.Items == nil {
			result.details.State = usage.DetailUnknown
		}
	}
	return result
}
