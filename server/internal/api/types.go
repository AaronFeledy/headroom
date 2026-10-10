package api

import (
	"context"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const DefaultVersion = "dev"

type Cache interface {
	Snapshot() []poller.Entry
	Get(string) (poller.Entry, bool)
}

type CursorCredentials interface {
	SetDesktopCookie(string, string)
	SetAccessToken(string) error
}

type GrokCredentials interface {
	SetCookieHeader(string)
}

type CodexResetter interface {
	ResetAttemptStatus(string) (known, blocked bool)
	ConsumeResetCredit(context.Context, string, string, string) (outcome string, ambiguous bool, err error)
}

type ProviderPoller interface {
	PollProvider(context.Context, string) (poller.Entry, bool, error)
	UpdateCredentials(context.Context, string, func() error) (poller.Entry, bool, error)
}

type Options struct {
	Cache                Cache
	Cursor               CursorCredentials
	Grok                 GrokCredentials
	Codex                CodexResetter
	Poller               ProviderPoller
	Logger               Logger
	AuthToken            string
	TLSCertificateSHA256 string
	Version              string
	ProviderNames        []string
}

type Logger interface {
	InfoContext(context.Context, string, ...any)
	ErrorContext(context.Context, string, ...any)
}

type healthResponse struct {
	Status    string           `json:"status"`
	Version   string           `json:"version"`
	Providers []providerHealth `json:"providers"`
}

type providerHealth struct {
	Name      string  `json:"name"`
	OK        bool    `json:"ok"`
	FetchedAt *string `json:"fetched_at"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type credentialResponse struct {
	Provider  string          `json:"provider"`
	Refetched bool            `json:"refetched"`
	Usage     usage.UsageData `json:"usage"`
}

func formatFetchedAt(t time.Time) *string {
	formatted := t.UTC().Format(time.RFC3339)
	return &formatted
}
