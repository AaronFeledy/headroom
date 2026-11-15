package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type credentials struct {
	accessToken  string
	refreshToken string
	userID       string
	clientID     string
	expiresAt    time.Time
	createTime   time.Time
	entryKey     string
}

type credentialStore struct {
	mu       sync.RWMutex
	refresh  sync.Mutex
	snapshot credstore.Snapshot
	creds    credentials
	loaded   bool
}

type refreshReason int

const (
	refreshReasonProactive refreshReason = iota
	refreshReasonUnauthorized
)

func (c credentials) needsRefresh(now time.Time) bool {
	expiresAt := c.effectiveExpiresAt()
	if c.refreshToken == "" || expiresAt.IsZero() {
		return false
	}
	return !now.Before(expiresAt)
}

func (c credentials) effectiveExpiresAt() time.Time {
	if !c.expiresAt.IsZero() {
		return c.expiresAt
	}
	if !c.createTime.IsZero() {
		return c.createTime.Add(30 * 24 * time.Hour)
	}
	return time.Time{}
}

func (p *Provider) credentials(ctx context.Context) (credentials, error) {
	p.store.mu.RLock()
	if p.store.loaded {
		creds := p.store.creds
		snapshot := p.store.snapshot
		p.store.mu.RUnlock()
		info, err := os.Stat(p.credentialsPath)
		if err != nil {
			return credentials{}, fmt.Errorf("stat Grok auth: %w", err)
		}
		state := credstore.RefreshState{Snapshot: snapshot, CurrentModTime: info.ModTime(), ExpiresAt: creds.effectiveExpiresAt()}
		if credstore.ShouldRefresh(p.now(), state) != credstore.RefreshDecisionReload {
			return creds, nil
		}
	} else {
		p.store.mu.RUnlock()
	}
	return p.loadCredentials(ctx)
}

func (p *Provider) loadCredentials(ctx context.Context) (credentials, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	snapshot, err := credstore.LoadSnapshot(ctx, p.credentialsPath)
	if err != nil {
		return credentials{}, fmt.Errorf("load Grok auth: %w", err)
	}
	creds, err := parseCredentials(snapshot.Data)
	if err != nil {
		return credentials{}, err
	}
	p.store.snapshot = snapshot
	p.store.creds = creds
	p.store.loaded = true
	return creds, nil
}

func parseCredentials(data []byte) (credentials, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return credentials{}, fmt.Errorf("parse Grok auth: %w", err)
	}
	for entryKey, raw := range root {
		if !strings.Contains(strings.ToLower(entryKey), "auth.x.ai") {
			continue
		}
		creds, err := parseCredentialEntry(entryKey, raw)
		if err != nil {
			return credentials{}, err
		}
		return creds, nil
	}
	return credentials{}, errMissingAuthEntry
}

func parseCredentialEntry(entryKey string, raw json.RawMessage) (credentials, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return credentials{}, fmt.Errorf("parse Grok auth entry: %w", err)
	}
	accessToken := jsonString(entry["key"])
	if strings.TrimSpace(accessToken) == "" {
		return credentials{}, errMissingAuthEntry
	}
	expiresAt, err := parseExpiresAt(entry["expires_at"])
	if err != nil {
		return credentials{}, err
	}
	createTime, err := parseCredentialTime(entry["create_time"], "create_time")
	if err != nil {
		return credentials{}, err
	}
	return credentials{
		accessToken:  strings.TrimSpace(accessToken),
		refreshToken: strings.TrimSpace(firstString(entry["refresh_token"], entry["refresh"])),
		userID:       strings.TrimSpace(jsonString(entry["user_id"])),
		clientID:     strings.TrimSpace(jsonString(entry["oidc_client_id"])),
		expiresAt:    expiresAt,
		createTime:   createTime,
		entryKey:     entryKey,
	}, nil
}

func parseExpiresAt(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return time.Time{}, nil
	}
	var millis int64
	if err := json.Unmarshal(raw, &millis); err == nil {
		return time.UnixMilli(millis).UTC(), nil
	}
	value := jsonString(raw)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse Grok auth expires_at: %w", err)
	}
	return parsed.UTC(), nil
}

func parseCredentialTime(raw json.RawMessage, field string) (time.Time, error) {
	value := jsonString(raw)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse Grok auth %s: %w", field, err)
	}
	return parsed.UTC(), nil
}

// refresh spends the caller's token pair through credstore.RefreshTokens. A
// file that already holds a different pair is adopted without an exchange.
func (p *Provider) refresh(ctx context.Context, stale credentials, reason refreshReason) (credentials, error) {
	p.store.refresh.Lock()
	defer p.store.refresh.Unlock()
	if reason == refreshReasonProactive && !stale.needsRefresh(p.now()) {
		return stale, nil
	}
	if strings.TrimSpace(stale.refreshToken) == "" {
		return credentials{}, errInvalidGrant
	}
	spent := credstore.TokenPair{Access: stale.accessToken, Refresh: stale.refreshToken}
	_, _, err := credstore.RefreshTokens(ctx, p.credentialsPath, spent, p.credentialCodec(), func(file []byte) (credentials, error) {
		current, err := parseCredentials(file)
		if err != nil {
			return credentials{}, err
		}
		if err := ctx.Err(); err != nil {
			return credentials{}, err
		}
		exchangeCtx, exchangeCancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
		defer exchangeCancel()
		refreshed, err := p.requestRefresh(exchangeCtx, current)
		if err != nil {
			return credentials{}, err
		}
		refreshed.entryKey = current.entryKey
		return refreshed, nil
	})
	if err != nil {
		if errors.Is(err, credstore.ErrNotWritable) {
			p.recordRefusal(err, spent)
		}
		return credentials{}, err
	}
	p.refusalLog.Saved()
	loadCtx, loadCancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer loadCancel()
	return p.loadCredentials(loadCtx)
}

func (p *Provider) credentialCodec() credstore.TokenCodec[credentials] {
	return credstore.TokenCodec[credentials]{
		Pair: func(file []byte) (credstore.TokenPair, error) {
			creds, err := parseCredentials(file)
			if err != nil {
				return credstore.TokenPair{}, err
			}
			return credstore.TokenPair{Access: creds.accessToken, Refresh: creds.refreshToken}, nil
		},
		Apply: func(file []byte, refreshed credentials) ([]byte, error) {
			return updateCredentialJSON(file, refreshed)
		},
		Of: func(refreshed credentials) credstore.TokenPair {
			return credstore.TokenPair{Access: refreshed.accessToken, Refresh: refreshed.refreshToken}
		},
		Warn: p.warnUnsaveable,
	}
}
