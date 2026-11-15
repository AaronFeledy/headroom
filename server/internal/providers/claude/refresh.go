package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type credentialUpdate struct {
	refreshed       refreshResponse
	expiresAtMillis int64
}

type sectionFields struct {
	access  string
	refresh string
	expires string
}

type refreshMode int

const (
	refreshModeProactive refreshMode = iota
	refreshModeForced
)

// refreshCredentials spends the caller's token pair. The shared refresher
// compares that pair with the file under the lock, so a sign-in another tool
// already refreshed is adopted without another POST.
func (c *Client) refreshCredentials(ctx context.Context, creds *credentials, mode refreshMode) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	store, writable := creds.store.(writableCredentialStore)
	if !writable {
		return ErrRefreshUnavailable
	}
	if mode == refreshModeProactive && creds.expiresAt.After(time.Now().UTC().Add(expirySkew)) {
		return nil
	}

	spent := credstore.TokenPair{Access: creds.accessToken, Refresh: creds.refreshToken}
	outcome, err := store.refresh(ctx, spent, func([]byte) (credentialUpdate, error) {
		refreshed, err := c.requestRefresh(ctx, spent.Refresh)
		if err != nil {
			return credentialUpdate{}, err
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = spent.Refresh
		}
		newExpiresAt := time.Now().UTC().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
		return credentialUpdate{refreshed: refreshed, expiresAtMillis: newExpiresAt.UnixMilli()}, nil
	})
	if err != nil {
		if errors.Is(err, credstore.ErrNotWritable) {
			c.refreshLog.Refused(creds.path, err, spent)
			return c.handleUnwritableRefresh(ctx, creds, mode, err)
		}
		c.refreshLog.NotSaved(creds.path, err)
		return err
	}
	if outcome == credstore.Saved {
		c.refreshLog.Saved()
	}

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()
	_, err = c.loadCredentials(loadCtx)
	return err
}

func (c *Client) handleUnwritableRefresh(ctx context.Context, current *credentials, mode refreshMode, cause error) error {
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()
	reloaded, err := c.loadCredentials(loadCtx)
	if err == nil {
		if reloaded.accessToken != current.accessToken || reloaded.refreshToken != current.refreshToken {
			return nil
		}
		if mode == refreshModeProactive && reloaded.expiresAt.After(time.Now().UTC()) {
			return nil
		}
	}
	return fmt.Errorf("%w: %w", ErrRefreshUnavailable, cause)
}

func (c *Client) requestRefresh(ctx context.Context, refreshToken string) (refreshResponse, error) {
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", oauthClientID)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.tokenURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return refreshResponse{}, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := ctx.Err(); err != nil {
		return refreshResponse{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if bytes.Contains(body, []byte("invalid_grant")) {
			return refreshResponse{}, ErrInvalidGrant
		}
		return refreshResponse{}, fmt.Errorf("refresh status %d: %w", resp.StatusCode, ErrUpstream)
	}
	var parsed refreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return refreshResponse{}, fmt.Errorf("parse refresh response: %w", err)
	}
	if parsed.AccessToken == "" || parsed.ExpiresIn == 0 {
		return refreshResponse{}, fmt.Errorf("refresh response missing fields: %w", ErrUpstream)
	}
	return parsed, nil
}

func applyCredentialUpdate(data []byte, source credentialSource, update credentialUpdate) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	key := "claudeAiOauth"
	fields := sectionFields{access: "accessToken", refresh: "refreshToken", expires: "expiresAt"}
	if source == credentialSourceOpenCode {
		key = "anthropic"
		fields = sectionFields{access: "access", refresh: "refresh", expires: "expires"}
	}
	section, err := updateSection(top[key], fields, update)
	if err != nil {
		return nil, err
	}
	top[key] = section
	return json.MarshalIndent(top, "", "  ")
}

func updateSection(raw json.RawMessage, fields sectionFields, update credentialUpdate) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, ErrCredentialsMalformed
	}
	var section map[string]json.RawMessage
	if err := json.Unmarshal(raw, &section); err != nil {
		return nil, fmt.Errorf("parse credential section: %w", err)
	}
	section[fields.access] = jsonString(update.refreshed.AccessToken)
	section[fields.refresh] = jsonString(update.refreshed.RefreshToken)
	section[fields.expires] = jsonNumber(update.expiresAtMillis)
	return json.Marshal(section)
}

func jsonString(value string) json.RawMessage {
	return json.RawMessage(strconv.Quote(value))
}

func jsonNumber(value int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(value, 10))
}
