package claude

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type bankCache struct {
	token       string
	status      *bankStatus
	attemptedAt time.Time
	goodAt      time.Time
	accountID   string
}

func applyBankHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
}

func (c *Client) readBankJSON(ctx context.Context, endpoint, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errResetUnavailable
	}
	applyBankHeaders(req, token)
	client := *c.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errResetUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errResetUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResetResponseBytes+1))
	if err != nil || len(body) > maxResetResponseBytes {
		return nil, errResetUnavailable
	}
	return body, nil
}

// bankMu serializes the token-bound status/profile cache, including reads.
func (c *Client) selectBankToken(token string) {
	if c.bank.token != token {
		c.bank = bankCache{token: token}
	}
}

func (c *Client) readBankStatus(ctx context.Context, token string) (*bankStatus, error) {
	body, err := c.readBankJSON(ctx, c.usageURL+"?cedar_ember=1&skip_spend=1", token)
	if err != nil {
		return nil, err
	}
	status := parseBankStatus(body)
	c.bank.status, c.bank.goodAt = status, time.Now()
	return status, nil
}

func (c *Client) bankAccount(ctx context.Context, token string) (string, error) {
	if c.bank.accountID != "" {
		return c.bank.accountID, nil
	}
	body, err := c.readBankJSON(ctx, c.apiBaseURL+"/api/oauth/profile", token)
	if err != nil {
		return "", err
	}
	var profile struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.Unmarshal(body, &profile) != nil || !uuidPattern.MatchString(profile.Account.UUID) || !uuidPattern.MatchString(profile.Organization.UUID) {
		return "", errResetUnavailable
	}
	c.bank.accountID = strings.ToLower(profile.Organization.UUID + "/" + profile.Account.UUID)
	return c.bank.accountID, nil
}

func accountFingerprint(accountID string) *string {
	org, account, ok := strings.Cut(accountID, "/")
	if !ok || !uuidPattern.MatchString(org) || !uuidPattern.MatchString(account) {
		return nil
	}
	digest := sha256.Sum256([]byte("headroom/claude/account-fingerprint/v1\x00" + org + "\x00" + account))
	value := hex.EncodeToString(digest[:])
	return &value
}

func matchesAccountFingerprint(accountID, expected string) bool {
	actual := accountFingerprint(accountID)
	return actual != nil && subtle.ConstantTimeCompare([]byte(*actual), []byte(expected)) == 1
}

func (c *Client) attachResetCredits(ctx context.Context, creds *credentials, data *usage.UsageData) {
	// All paths that can reconcile or submit use this order. The cross-process
	// lock is acquired before any bank GET that may reconcile a pending attempt.
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	c.bankMu.Lock()
	defer c.bankMu.Unlock()
	journal, lock, journalErr := c.openResetJournal(ctx, false)
	if lock != nil {
		defer lock.Close()
	}
	c.selectBankToken(creds.accessToken)
	now := time.Now()
	interval := 5 * time.Minute
	for _, bucket := range data.Buckets {
		if (bucket.ID == usage.BucketSession || bucket.ID == usage.BucketWeekly) && bucket.Utilization >= 95 {
			interval = time.Minute
		}
	}
	fresh := false
	if now.Sub(c.bank.attemptedAt) >= interval {
		c.bank.attemptedAt = now
		status, err := c.readBankStatus(ctx, creds.accessToken)
		fresh = err == nil && status != nil
	}
	if time.Since(c.bank.goodAt) <= 15*time.Minute {
		data.RateLimitResetCredits = c.bank.status.credits(time.Now())
	}
	if credits := data.RateLimitResetCredits; credits != nil {
		if account, err := c.bankAccount(ctx, creds.accessToken); err == nil {
			data.ProviderAccountID = account
			credits.AccountFingerprint = accountFingerprint(account)
			if fingerprint := credits.AccountFingerprint; fingerprint != nil {
				if journalErr == nil && fresh {
					_ = reconcileResetJournal(journal, credits, *fingerprint, c.commitResetJournal)
				}
				binding := *fingerprint
				credits.ReconciledRequests = func() []string { return c.reconciledResetRequests(binding) }
			}
		}
	}
}
