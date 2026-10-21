package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const maxResetResponseBytes = 64 << 10
const maxRememberedResetAttempts = 1024

var errResetUnavailable = errors.New("reset request unavailable")
var errDifferentResetPending = errors.New("a different reset request is awaiting usage confirmation")
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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

func (c *Client) observeResetStatus(credits *usage.RateLimitResetCredits, accountID string, started time.Time) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	if credits != nil && c.resetAttemptID != "" && accountID == c.resetAccountID && started.After(c.resetStartedAt) && (credits.AvailableCount < c.resetAvailable || credits.UsableNow != nil && !*credits.UsableNow) {
		c.resetUsageObserved = true
	}
}

// ConsumeResetCredit can spend a valuable banked reset. Do not test this
// method, the endpoint it calls, or code that may trigger it: even a test can
// consume a real reset. The skipped safety test is intentional, not a TODO.
func (c *Client) ConsumeResetCredit(ctx context.Context, requestID, expectedAccountID, expectedAccountFingerprint string) (outcome string, ambiguous bool, err error) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	requestID = strings.ToLower(requestID)
	if !requestIDPattern.MatchString(requestID) {
		return "", false, errResetUnavailable
	}
	creds, err := c.currentCredentials(ctx)
	if err != nil {
		return "", false, errResetUnavailable
	}
	creds, err = c.ensureFreshCredentials(ctx, creds)
	if err != nil {
		return "", false, errResetUnavailable
	}
	c.bankMu.Lock()
	defer c.bankMu.Unlock()
	c.selectBankToken(creds.accessToken)
	accountID, err := c.bankAccount(ctx, creds.accessToken)
	if err != nil || !matchesAccountFingerprint(accountID, expectedAccountFingerprint) || expectedAccountID != "" && expectedAccountID != accountID {
		return "", false, errResetUnavailable
	}
	if previous, exists := c.resetAttempts[requestID]; exists {
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
	if c.resetAttemptID != "" && !c.resetUsageObserved {
		return "", false, errDifferentResetPending
	}
	if expectedAccountID == "" || len(c.resetAttempts) >= maxRememberedResetAttempts {
		return "", false, errResetUnavailable
	}
	c.bank.attemptedAt = time.Now()
	status, err := c.readBankStatus(ctx, creds.accessToken)
	if err != nil {
		return "", false, errResetUnavailable
	}
	credits := status.credits(time.Now())
	if credits == nil || credits.AvailableCount <= 0 || !*credits.UsableNow {
		return "", false, errResetUnavailable
	}
	org, _, ok := strings.Cut(accountID, "/")
	if !ok || !uuidPattern.MatchString(org) {
		return "", false, errResetUnavailable
	}
	body, err := json.Marshal(struct {
		Program   string `json:"program"`
		GrantID   string `json:"grant_id"`
		RequestID string `json:"request_id"`
	}{"cedar_ember", status.nextGrantID, requestID})
	if err != nil {
		return "", false, errResetUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/api/organizations/"+org+"/reset_rate_limits", bytes.NewReader(body))
	if err != nil {
		return "", false, errResetUnavailable
	}
	// Never permit upload replay or redirects for this valuable mutation.
	req.GetBody = nil
	applyBankHeaders(req, creds.accessToken)
	req.Header.Set("Content-Type", "application/json")
	client := *c.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	// Remember before submission; uncertain outcomes must never become replayable.
	c.resetAttemptID, c.resetAccountID = requestID, accountID
	c.resetStartedAt, c.resetAvailable, c.resetUsageObserved = time.Now(), credits.AvailableCount, false
	if c.resetAttempts == nil {
		c.resetAttempts = make(map[string]resetAttempt)
	}
	c.resetAttempts[requestID] = resetAttempt{accountID: accountID}
	c.bank.attemptedAt = time.Time{}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, errResetUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
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
		Result string `json:"result"`
	}
	if json.Unmarshal(responseBody, &decoded) != nil {
		return "", true, errResetUnavailable
	}
	switch decoded.Result {
	case "reset":
		outcome = "reset"
	case "already_used":
		outcome = "already_redeemed"
	case "not_limited":
		outcome, c.resetUsageObserved = "nothing_to_reset", true
	case "cooldown", "ineligible", "unavailable":
		outcome, c.resetUsageObserved = "unavailable", true
	default:
		return "", true, errResetUnavailable
	}
	c.resetAttempts[requestID] = resetAttempt{accountID: accountID, outcome: outcome}
	return outcome, false, nil
}
