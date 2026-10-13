package cursor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func (c *Client) Fetch(ctx context.Context) (usage.UsageData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := baseUsageData()
	previousStamps := maps.Clone(c.stamps)
	candidates := c.candidates(ctx)
	order := make([]int, 0, len(candidates))
	activeIndex := -1
	for index, candidate := range candidates {
		if candidate.cookie == c.active.cookie && candidate.status == "signed_in" {
			activeIndex = index
			break
		}
	}
	if activeIndex >= 0 {
		for index := 0; index < activeIndex; index++ {
			if stamp(candidates[index].path) != previousStamps[candidates[index].path] || stamp(candidates[index].path+"-wal") != previousStamps[candidates[index].path+"-wal"] {
				order = append(order, index)
			}
		}
		order = append(order, activeIndex)
	}
	for index := range candidates {
		found := false
		for _, existing := range order {
			if existing == index {
				found = true
			}
		}
		if !found {
			order = append(order, index)
		}
	}
	for _, candidate := range candidates {
		if candidate.path != "" {
			c.stamps[candidate.path] = stamp(candidate.path)
		}
	}
	for _, index := range order {
		candidate := candidates[index]
		if candidate.cookie != "" && c.isRejected(candidate.cookie) {
			candidates[index].status = "expired"
			continue
		}
		if candidate.status != "signed_in" || candidate.cookie == "" {
			continue
		}
		cookieHeader := candidate.cookie
		summary, status, err := c.fetchUsageSummary(ctx, cookieHeader)
		if status == http.StatusUnauthorized || errors.Is(err, ErrUnauthorized) {
			c.reject(cookieHeader)
			candidates[index].status = "expired"
			if c.active.cookie == "" {
				c.active = candidate
			}
			c.secret.clear()
			continue
		}
		c.adopt(candidate)
		data = c.attachAuth(data, candidates, &candidate)
		data.CredentialEpoch = usage.CredentialEpoch(providerName, candidate.source.Kind, candidate.source.Name, candidate.path, cookieHeader)
		if err != nil {
			var typed *usage.FetchFailure
			if errors.As(err, &typed) {
				data.FetchFailure = typed
			}
			message := "Cursor usage request failed. Will retry."
			if usage.IsTimeout(err) {
				message = "Cursor usage request timed out. Will retry."
			} else if errors.Is(err, context.Canceled) {
				message = "Cursor usage request canceled."
			}
			data.Error = &message
			if ctx.Err() != nil {
				return data, err
			}
			return data, nil
		}
		if status != http.StatusOK {
			message := fmt.Sprintf("Cursor usage request failed with HTTP %d.", status)
			data.Error = &message
			return data, nil
		}
		userInfo := c.fetchUserInfo(ctx, cookieHeader)
		legacyUsage := c.fetchLegacyUsage(ctx, cookieHeader, userInfo.Sub)
		sand := c.fetchSandUsage(ctx, cookieHeader)
		populateUsageData(&data, summary, legacyUsage, sand)
		data.Subtitle = summary.MembershipType
		return data, nil
	}
	c.secret.clear()
	for _, candidate := range candidates {
		if candidate.failure != nil {
			data.FetchFailure = candidate.failure
			break
		}
	}
	return c.attachAuth(data, candidates, nil), ctx.Err()
}
