package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	providerName     = "Cursor"
	defaultBaseURL   = "https://cursor.com"
	usageSummaryPath = "/api/usage-summary"
	authMePath       = "/api/auth/me"
	legacyUsagePath  = "/api/usage"
)

var (
	ErrInvalidToken = errors.New("cursor: invalid access token")
	ErrUnauthorized = errors.New("cursor: unauthorized")
)

type Options struct {
	BaseURL            string
	AuthPath           string
	HTTPClient         *http.Client
	Discovery          *Discovery
	Logger             *slog.Logger
	BrowserCredentials *bool
}

type Client struct {
	baseURL        string
	authPath       string
	httpClient     *http.Client
	secret         secretStore
	mu             sync.Mutex
	discovery      Discovery
	logger         *slog.Logger
	active         credential
	pushed         credential
	rejected       [][32]byte
	browsers       []credential
	browserScanned time.Time
	stamps         map[string]fileStamp
}

type secretStore struct {
	mu           sync.RWMutex
	cookieHeader string
}

func NewClient(opts Options) *Client {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	discovery := defaultDiscovery()
	if opts.Discovery != nil {
		discovery = *opts.Discovery
	}
	if opts.BrowserCredentials != nil {
		discovery.BrowserCredentials = *opts.BrowserCredentials
	}
	if discovery.Now == nil {
		discovery.Now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		discovery: discovery, logger: logger, stamps: map[string]fileStamp{},
		baseURL:    baseURL,
		authPath:   opts.AuthPath,
		httpClient: httpClient,
	}
}

func (c *Client) Name() string { return providerName }

func (c *Client) SetCookieHeader(cookieHeader string) {
	c.SetDesktopCookie(cookieHeader, "browser")
}

func (c *Client) SetDesktopCookie(cookieHeader, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name == "" {
		name = "browser"
	}
	cookie := strings.TrimSpace(cookieHeader)
	if cookieExpired(cookie, c.discovery.Now()) {
		c.reject(cookie)
		return
	}
	if c.isRejected(cookie) {
		return
	}
	c.pushed = credential{source: usage.AuthSource{Kind: "desktop", Name: name}, cookie: cookie, status: "signed_in"}
	c.secret.set(cookie)
}

func (c *Client) SetAccessToken(accessToken string) error {
	cookieHeader, err := cookieFromAccessToken(accessToken)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cookieExpired(cookieHeader, c.discovery.Now()) {
		c.reject(cookieHeader)
		return nil
	}
	if c.isRejected(cookieHeader) {
		return nil
	}
	c.pushed = credential{source: usage.AuthSource{Kind: "api", Name: "API"}, cookie: cookieHeader, status: "signed_in"}
	c.secret.set(cookieHeader)
	return nil
}

func (c *Client) fetchUsageSummary(ctx context.Context, cookieHeader string) (cursorUsageSummary, int, error) {
	resp, err := c.get(ctx, usageSummaryPath, cookieHeader)
	if err != nil {
		return cursorUsageSummary{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cursorUsageSummary{}, resp.StatusCode, nil
	}
	var summary cursorUsageSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return cursorUsageSummary{}, resp.StatusCode, fmt.Errorf("decode Cursor usage summary: %w", err)
	}
	return summary, resp.StatusCode, nil
}

func (c *Client) fetchUserInfo(ctx context.Context, cookieHeader string) cursorUserInfo {
	resp, err := c.get(ctx, authMePath, cookieHeader)
	if err != nil {
		return cursorUserInfo{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cursorUserInfo{}
	}
	var userInfo cursorUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return cursorUserInfo{}
	}
	return userInfo
}

func (c *Client) fetchLegacyUsage(ctx context.Context, cookieHeader string, userSub string) *cursorUsageResponse {
	if strings.TrimSpace(userSub) == "" {
		return nil
	}
	path := legacyUsagePath + "?user=" + url.QueryEscape(userSub)
	resp, err := c.get(ctx, path, cookieHeader)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var legacyUsage cursorUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&legacyUsage); err != nil {
		return nil
	}
	return &legacyUsage
}

func (c *Client) get(ctx context.Context, path string, cookieHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build Cursor request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookieHeader)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Cursor request: %w", err)
	}
	return resp, nil
}

func (s *secretStore) set(cookieHeader string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookieHeader = cookieHeader
}

func (s *secretStore) get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cookieHeader
}

func (s *secretStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookieHeader = ""
}

func (c *Client) hasSecret() bool {
	return c.secret.get() != ""
}

func baseUsageData() usage.UsageData {
	return usage.UsageData{
		ProviderName:   providerName,
		PrimaryLabel:   "Included Plan",
		SecondaryLabel: "On-Demand",
		ShowSecondary:  true,
		ReauthCommand:  nil,
		NeedsReauth:    false,
		Current:        usage.UsageBucket{},
		Weekly:         usage.UsageBucket{},
	}
}
