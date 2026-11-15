package codex

import (
	"log/slog"
	"net/http"
	"time"
)

const (
	providerName        = "Codex"
	defaultUsageURL     = "https://chatgpt.com/backend-api/wham/usage"
	defaultTokenURL     = "https://auth.openai.com/oauth/token"
	defaultResetURL     = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	oauthClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthScope          = "openid profile email"
	userAgent           = "ClaudeUsageWidget"
	defaultHTTPTimeout  = 10 * time.Second
	tokenRequestTimeout = 30 * time.Second
)

type Options struct {
	CredentialsPath string
	UsageURL        string
	TokenURL        string
	HTTPClient      *http.Client
	Discovery       DiscoveryOptions
	Logger          *slog.Logger
}

type CredentialSource int

const (
	CredentialSourceCodex CredentialSource = iota
	CredentialSourceOpenCode
)

type Credentials struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	Source       CredentialSource
	Path         string
	LastRefresh  *time.Time
	ExpiresAt    *time.Time
}

type RefreshedTokens struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    time.Time
	RefreshedAt  time.Time
}

type CredentialOptions struct {
	Path      string
	Source    CredentialSource
	Discovery DiscoveryOptions
}
