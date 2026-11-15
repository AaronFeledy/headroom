package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func (c *Client) refresh(ctx context.Context, creds Credentials) (usage.RefreshResult, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return usage.RefreshInvalidGrant, nil
	}
	result, err := c.doRefresh(ctx, creds)
	switch {
	case err == nil:
		c.refreshLog.Saved()
	case errors.Is(err, credstore.ErrNotWritable):
		c.refreshLog.Refused(creds.Path, err, tokenPair(creds))
	default:
		c.refreshLog.NotSaved(creds.Path, err)
	}
	return result, err
}

func (c *Client) doRefresh(ctx context.Context, stale Credentials) (usage.RefreshResult, error) {
	err := c.store.Refresh(ctx, stale, func(current Credentials) (RefreshedTokens, error) {
		if err := ctx.Err(); err != nil {
			return RefreshedTokens{}, err
		}
		exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
		defer cancel()
		return c.exchangeRefresh(exchangeCtx, current)
	})
	if err != nil {
		if errors.Is(err, ErrRefreshInvalidGrant) {
			return usage.RefreshInvalidGrant, nil
		}
		if errors.Is(err, ErrRefreshFailed) {
			return usage.RefreshFailed, nil
		}
		return usage.RefreshFailed, err
	}
	return usage.RefreshSuccess, nil
}

func (c *Client) exchangeRefresh(ctx context.Context, creds Credentials) (RefreshedTokens, error) {
	body, err := json.Marshal(refreshRequest{ClientID: oauthClientID, GrantType: "refresh_token", RefreshToken: creds.RefreshToken, Scope: oauthScope})
	if err != nil {
		return RefreshedTokens{}, fmt.Errorf("encode codex refresh request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(body))
	if err != nil {
		return RefreshedTokens{}, fmt.Errorf("build codex refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return RefreshedTokens{}, err
	}
	responseBody, readErr := io.ReadAll(resp.Body)
	closeErr := closeResponseBody(resp, "close codex refresh response body")
	if readErr != nil {
		return RefreshedTokens{}, errors.Join(fmt.Errorf("read codex refresh response: %w", readErr), closeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusUnauthorized || isInvalidGrantBody(responseBody) {
			return RefreshedTokens{}, errors.Join(ErrRefreshInvalidGrant, closeErr)
		}
		return RefreshedTokens{}, errors.Join(ErrRefreshFailed, closeErr)
	}
	var decoded refreshResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return RefreshedTokens{}, errors.Join(fmt.Errorf("decode codex refresh response: %w", err), closeErr)
	}
	accessToken := decoded.AccessToken
	if accessToken == "" {
		accessToken = creds.AccessToken
	}
	if strings.TrimSpace(accessToken) == "" {
		return RefreshedTokens{}, errors.Join(ErrRefreshFailed, closeErr)
	}
	refreshToken := decoded.RefreshToken
	if refreshToken == "" {
		refreshToken = creds.RefreshToken
	}
	expiresAt := credsExpiresAt(creds)
	if decoded.ExpiresIn != nil {
		expiresAt = time.Now().UTC().Add(time.Duration(*decoded.ExpiresIn) * time.Second)
	}
	return RefreshedTokens{
		AccessToken: accessToken, RefreshToken: refreshToken, AccountID: creds.AccountID,
		ExpiresAt: expiresAt, RefreshedAt: time.Now().UTC(),
	}, nil
}
