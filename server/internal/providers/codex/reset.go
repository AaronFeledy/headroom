package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const maxResetResponseBytes = 64 << 10
const maxRememberedResetAttempts = 1024
const accountFingerprintDomain = "headroom/codex/account-fingerprint/v1\x00"

var errResetUnavailable = errors.New("reset request unavailable")
var errDifferentResetPending = errors.New("a different reset request is awaiting usage confirmation")

type resetAttempt struct {
	accountID string
	outcome   string
}

func (c *Client) ResetAttemptStatus(requestID string) (known, blocked bool) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	if _, exists := c.resetAttempts[strings.ToLower(requestID)]; exists {
		return true, false
	}
	return false, c.resetAttemptID != "" && !c.resetUsageObserved
}

// Only a successful provider GET begun after submission can establish that
// this account's weekly usage fell below the manual reset floor. An older in-flight fetch, a new
// reset timestamp, or a different account must not release the pending gate.
// This observation never retries or consumes a reset.
func (c *Client) observeResetUsage(data usage.UsageData, fetchStartedAt time.Time) {
	if data.Error != nil {
		return
	}
	for _, bucket := range data.Buckets {
		if bucket.ID != usage.BucketWeekly || math.IsNaN(bucket.Utilization) || math.IsInf(bucket.Utilization, 0) || bucket.Utilization < 0 || bucket.Utilization >= usage.CodexManualResetFloor {
			continue
		}
		c.resetMu.Lock()
		if c.resetAttemptID != "" && c.resetAccountID == strings.TrimSpace(data.ProviderAccountID) && fetchStartedAt.After(c.resetStartedAt) {
			c.resetUsageObserved = true
		}
		c.resetMu.Unlock()
		return
	}
}

// ConsumeResetCredit can spend a valuable banked reset. Do not test this
// method, the endpoint it calls, or code that may trigger it: even a test can
// consume a real reset. The skipped safety test is intentional, not a TODO.
func (c *Client) ConsumeResetCredit(ctx context.Context, requestID, expectedAccountID, expectedAccountFingerprint string) (outcome string, ambiguous bool, err error) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	requestID = strings.ToLower(requestID)
	creds, err := c.store.Current(ctx)
	accountID := strings.TrimSpace(creds.AccountID)
	if err != nil || accountID == "" || !matchesAccountFingerprint(accountID, expectedAccountFingerprint) {
		return "", false, errResetUnavailable
	}
	if expectedAccountID != "" && strings.TrimSpace(expectedAccountID) != accountID {
		return "", false, errResetUnavailable
	}
	if previous, exists := c.resetAttempts[requestID]; exists {
		// Every ID is submitted at most once in this server process. Even an
		// uncertain result is never retried; repeated requests only read this
		// record. Keep older IDs after a later usage cycle begins as well.
		if previous.accountID != accountID {
			return "", false, errResetUnavailable
		}
		if previous.outcome == "" {
			return "", true, errResetUnavailable
		}
		if previous.outcome == "reset" {
			return "already_redeemed", false, nil
		}
		return previous.outcome, false, nil
	}
	if c.resetAttemptID != "" && c.resetAttemptID != requestID && !c.resetUsageObserved {
		return "", false, errDifferentResetPending
	}
	if c.resetAttemptID != requestID && expectedAccountID == "" {
		return "", false, errResetUnavailable
	}
	if len(c.resetAttempts) >= maxRememberedResetAttempts {
		// Never evict an attempted ID and accidentally make it replayable.
		return "", false, errResetUnavailable
	}
	body, err := json.Marshal(struct {
		RedeemRequestID string `json:"redeem_request_id"`
	}{RedeemRequestID: requestID})
	if err != nil {
		return "", false, errResetUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultResetURL, bytes.NewReader(body))
	if err != nil {
		return "", false, errResetUnavailable
	}
	// A non-rewindable POST body prevents Go's transport from replaying this
	// mutation after a connection failure. Redirects remain disabled as well.
	req.GetBody = nil
	applyUsageHeaders(req, creds)
	req.Header.Set("Content-Type", "application/json")
	client := *c.http
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	// From this point onward the request may have reached the provider. Keep the
	// ID permanently in this process; no retry is permitted. Only a later
	// provider reading can release the gate for a new, explicitly chosen reset.
	// DO NOT exercise this path to test either behavior.
	c.resetAttemptID = requestID
	c.resetAccountID = accountID
	c.resetStartedAt = time.Now()
	c.resetUsageObserved = false
	if c.resetAttempts == nil {
		c.resetAttempts = make(map[string]resetAttempt)
	}
	c.resetAttempts[requestID] = resetAttempt{accountID: accountID}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return "", true, errResetUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResetResponseBytes))
		return "", true, errResetUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return "", true, errResetUnavailable
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResetResponseBytes+1))
	if err != nil || len(responseBody) > maxResetResponseBytes {
		return "", true, errResetUnavailable
	}
	var decoded struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil || !validResetOutcome(decoded.Code) {
		return "", true, errResetUnavailable
	}
	c.resetAttempts[requestID] = resetAttempt{accountID: accountID, outcome: decoded.Code}
	return decoded.Code, false, nil
}

func accountFingerprint(accountID string) *string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(accountFingerprintDomain + accountID))
	value := fmt.Sprintf("%x", digest)
	return &value
}

func matchesAccountFingerprint(accountID, expected string) bool {
	actual := accountFingerprint(accountID)
	return actual != nil && subtle.ConstantTimeCompare([]byte(*actual), []byte(expected)) == 1
}

func validResetOutcome(outcome string) bool {
	switch outcome {
	case "reset", "already_redeemed", "nothing_to_reset", "no_credit":
		return true
	default:
		return false
	}
}

func applyUsageHeaders(req *http.Request, creds Credentials) {
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(creds.AccountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", creds.AccountID)
	}
}
