package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const maxResetResponseBytes = 64 << 10
const maxRememberedResetAttempts = 1024

var errResetUnavailable = errors.New("reset request unavailable")
var errDifferentResetPending = errors.New("a different reset request is awaiting usage confirmation")

func (c *Client) ResetAttemptStatus(requestID string) (known, blocked bool) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	journal, lock, err := c.openResetJournal(ctx, true)
	if err != nil {
		return false, true
	}
	if lock.Close() != nil {
		return false, true
	}
	if _, exists := journal.Attempts[strings.ToLower(requestID)]; exists {
		return true, false
	}
	// This API has no account argument, so any unresolved account blocks a new ID.
	return false, journal.pending() || len(journal.Attempts) >= maxRememberedResetAttempts
}

// ConsumeResetCredit can spend a valuable banked reset. Do not test this
// method, the endpoint it calls, or code that may trigger it: even a test can
// consume a real reset. The skipped safety test is intentional, not a TODO.
func (c *Client) ConsumeResetCredit(ctx context.Context, requestID, expectedAccountID, expectedAccountFingerprint string) (outcome string, ambiguous bool, err error) {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	requestID = strings.ToLower(requestID)
	if !validRequestID(requestID) {
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
	journal, lock, err := c.openResetJournal(ctx, true)
	if err != nil {
		return "", false, errResetUnavailable
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errResetUnavailable
			ambiguous = true
		}
	}()
	c.selectBankToken(creds.accessToken)
	accountID, err := c.bankAccount(ctx, creds.accessToken)
	if err != nil || !matchesAccountFingerprint(accountID, expectedAccountFingerprint) || expectedAccountID != "" && expectedAccountID != accountID {
		return "", false, errResetUnavailable
	}
	if previous, exists := journal.Attempts[requestID]; exists {
		if !matchesAccountFingerprint(accountID, previous.AccountFingerprint) {
			return "", false, errResetUnavailable
		}
		if previous.Outcome == "" {
			return "", true, errResetUnavailable
		}
		if previous.Outcome == "reset" {
			return "already_redeemed", false, nil
		}
		return previous.Outcome, false, nil
	}
	if journal.pending() {
		return "", false, errDifferentResetPending
	}
	if expectedAccountID == "" || len(journal.Attempts) >= maxRememberedResetAttempts {
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
	// Commit the FINAL bank baseline before the only upstream submission. The
	// cross-process lock remains held through the response and terminal commit.
	// Crashing here intentionally leaves a pending attempt even before send.
	attempt := resetAttempt{AccountFingerprint: *accountFingerprint(accountID), AvailableBefore: credits.AvailableCount, Phase: "pending"}
	journal.Attempts[requestID] = attempt
	if c.commitResetJournal(journal) != nil {
		return "", false, errResetUnavailable
	}
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
		outcome = "nothing_to_reset"
	case "cooldown", "ineligible", "unavailable":
		outcome = "unavailable"
	default:
		return "", true, errResetUnavailable
	}
	attempt.Outcome = outcome
	journal.Attempts[requestID] = attempt
	if c.commitResetJournal(journal) != nil {
		return "", true, errResetUnavailable
	}
	return outcome, false, nil
}
