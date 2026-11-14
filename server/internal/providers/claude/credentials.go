package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type credentialSource int

const (
	credentialSourceClaude credentialSource = iota
	credentialSourceOpenCode
)

type discoveryOptions struct {
	configuredPath string
	configDir      string
	homeDir        string
	wslHomeDir     string
	openCodePath   string
}

type credentials struct {
	path             string
	accessToken      string
	refreshToken     string
	subscriptionType *string
	expiresAt        time.Time
	source           credentialSource
	snapshot         credstore.Snapshot
	store            credentialStore
	rediscover       bool
}

type claudeCredentialShape struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	SubscriptionType string `json:"subscriptionType"`
	RateLimitTier    string `json:"rateLimitTier"`
}

type openCodeCredentialShape struct {
	AccessToken  string `json:"access"`
	RefreshToken string `json:"refresh"`
	ExpiresAt    int64  `json:"expires"`
}

type parsedCredentialFields struct {
	accessToken      string
	refreshToken     string
	expiresAtMillis  int64
	subscriptionType *string
}

type credentialIdentity struct {
	path   string
	source credentialSource
}

func parseCredentials(path string, source credentialSource, snapshot credstore.Snapshot) (*credentials, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Data, &top); err != nil {
		return nil, credentialsError{path: path, err: fmt.Errorf("%w: %v", ErrCredentialsMalformed, err)}
	}
	switch source {
	case credentialSourceClaude:
		if _, ok := top["claudeAiOauth"]; !ok {
			if _, ok := top["anthropic"]; ok {
				return parseOpenCodeCredentials(path, snapshot, top)
			}
		}
		return parseClaudeCredentials(path, snapshot, top)
	case credentialSourceOpenCode:
		return parseOpenCodeCredentials(path, snapshot, top)
	default:
		return nil, credentialsError{path: path, err: ErrCredentialsMalformed}
	}
}

func parseClaudeCredentials(path string, snapshot credstore.Snapshot, top map[string]json.RawMessage) (*credentials, error) {
	raw, ok := top["claudeAiOauth"]
	if !ok {
		return nil, credentialsError{path: path, err: fmt.Errorf("claudeAiOauth missing: %w", ErrCredentialsMalformed)}
	}
	var shape claudeCredentialShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, credentialsError{path: path, err: fmt.Errorf("claudeAiOauth: %w", ErrCredentialsMalformed)}
	}
	subscription := normalizeSubscriptionType(shape.SubscriptionType)
	if subscription == nil {
		subscription = inferSubscriptionType(shape.RateLimitTier)
	}
	return newCredentials(credentialIdentity{path: path, source: credentialSourceClaude}, snapshot, parsedCredentialFields{accessToken: shape.AccessToken, refreshToken: shape.RefreshToken, expiresAtMillis: shape.ExpiresAt, subscriptionType: subscription})
}

func parseOpenCodeCredentials(path string, snapshot credstore.Snapshot, top map[string]json.RawMessage) (*credentials, error) {
	raw, ok := top["anthropic"]
	if !ok {
		return nil, credentialsError{path: path, err: fmt.Errorf("anthropic missing: %w", ErrCredentialsMalformed)}
	}
	var shape openCodeCredentialShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, credentialsError{path: path, err: fmt.Errorf("anthropic: %w", ErrCredentialsMalformed)}
	}
	return newCredentials(credentialIdentity{path: path, source: credentialSourceOpenCode}, snapshot, parsedCredentialFields{accessToken: shape.AccessToken, refreshToken: shape.RefreshToken, expiresAtMillis: shape.ExpiresAt})
}

func newCredentials(identity credentialIdentity, snapshot credstore.Snapshot, fields parsedCredentialFields) (*credentials, error) {
	if fields.accessToken == "" || fields.refreshToken == "" || fields.expiresAtMillis == 0 {
		return nil, credentialsError{path: identity.path, err: ErrCredentialsMalformed}
	}
	return &credentials{path: identity.path, accessToken: fields.accessToken, refreshToken: fields.refreshToken, subscriptionType: fields.subscriptionType, expiresAt: time.UnixMilli(fields.expiresAtMillis).UTC(), source: identity.source, snapshot: snapshot}, nil
}

func normalizeSubscriptionType(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	lower := strings.ToLower(trimmed)
	name := strings.ToUpper(lower[:1]) + lower[1:]
	return &name
}

func inferSubscriptionType(rateLimitTier string) *string {
	lower := strings.ToLower(rateLimitTier)
	for _, tier := range []string{"max", "pro", "free"} {
		if strings.Contains(lower, tier) {
			return normalizeSubscriptionType(tier)
		}
	}
	return nil
}

func openCodePath(opts discoveryOptions, wslHome string) string {
	if opts.openCodePath != "" {
		return opts.openCodePath
	}
	if wslHome != "" {
		return filepath.Join(wslHome, ".local", "share", "opencode", "auth.json")
	}
	return filepath.Join(homeDir(opts.homeDir), ".local", "share", "opencode", "auth.json")
}

func homeDir(configured string) string {
	if configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return home
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("USERPROFILE")
	}
	return os.Getenv("HOME")
}

func expandHome(path string, home string) string {
	if path == "~" {
		return homeDir(home)
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(homeDir(home), path[2:])
	}
	return path
}
