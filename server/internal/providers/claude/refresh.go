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

func (c *Client) refreshCredentials(ctx context.Context, creds *credentials, mode refreshMode) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	current, err := c.loadCredentials(ctx)
	if err != nil {
		return err
	}
	if current.accessToken != creds.accessToken || current.refreshToken != creds.refreshToken {
		return nil
	}
	if _, writable := current.store.(writableCredentialStore); !writable {
		return ErrKeychainExpired
	}
	if mode == refreshModeProactive && current.expiresAt.After(time.Now().UTC().Add(expirySkew)) {
		return nil
	}
	refreshed, err := c.requestRefresh(ctx, current.refreshToken)
	if err != nil {
		return err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = current.refreshToken
	}
	newExpiresAt := time.Now().UTC().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	if err := c.writeCredentials(ctx, current, credentialUpdate{refreshed: refreshed, expiresAtMillis: newExpiresAt.UnixMilli()}); err != nil && !errors.Is(err, errCredentialsChanged) {
		return err
	}
	_, err = c.loadCredentials(ctx)
	return err
}

func (c *Client) requestRefresh(ctx context.Context, refreshToken string) (refreshResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", oauthClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return refreshResponse{}, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

func (c *Client) writeCredentials(ctx context.Context, creds *credentials, update credentialUpdate) error {
	store, writable := creds.store.(writableCredentialStore)
	if !writable {
		return ErrKeychainExpired
	}
	return store.update(ctx, func(data []byte) ([]byte, error) {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(data, &top); err != nil {
			return nil, fmt.Errorf("parse credentials: %w", err)
		}
		key := "claudeAiOauth"
		fields := sectionFields{access: "accessToken", refresh: "refreshToken", expires: "expiresAt"}
		if creds.source == credentialSourceOpenCode {
			key = "anthropic"
			fields = sectionFields{access: "access", refresh: "refresh", expires: "expires"}
		}
		var stored map[string]json.RawMessage
		if err := json.Unmarshal(top[key], &stored); err != nil {
			return nil, ErrCredentialsMalformed
		}
		var refreshToken string
		if err := json.Unmarshal(stored[fields.refresh], &refreshToken); err != nil {
			return nil, ErrCredentialsMalformed
		}
		if refreshToken != creds.refreshToken {
			return nil, errCredentialsChanged
		}
		section, err := updateSection(top[key], fields, update)
		if err != nil {
			return nil, err
		}
		top[key] = section
		return json.MarshalIndent(top, "", "  ")
	})
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
