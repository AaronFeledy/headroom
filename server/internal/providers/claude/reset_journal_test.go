package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// These tests only seed disk state, inspect policy, serialize usage, and GET
// status. Never call a consumer, prepare/arm an action, or invoke reset routes.
const journalFixtureID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const journalNextID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
const journalAccount = "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"

func seedJournal(t *testing.T, c *Client, journal resetJournal) {
	t.Helper()
	if c.resetStateDir == "" {
		c.resetStateDir = filepath.Join(t.TempDir(), "state")
	}
	if err := os.MkdirAll(c.resetStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.resetStateDir, resetJournalName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pendingJournal(account string, available int64) resetJournal {
	return resetJournal{Version: 1, Attempts: map[string]resetAttempt{journalFixtureID: {
		AccountFingerprint: *accountFingerprint(account), AvailableBefore: available, Phase: "pending",
	}}}
}

func seedPendingJournal(t *testing.T, c *Client, account string, available int64) {
	t.Helper()
	seedJournal(t, c, pendingJournal(account, available))
}

func encodedEvidence(t *testing.T, data usage.UsageData) []string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Credits struct {
			IDs []string `json:"reconciled_request_ids"`
		} `json:"rate_limit_reset_credits"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Credits.IDs
}

func Test_ResetJournal_final_baseline_and_fresh_bank_only(t *testing.T) {
	otherAccount := "11111111-1111-1111-1111-111111111111/33333333-3333-3333-3333-333333333333"
	for _, tc := range []struct {
		name           string
		count          int
		usable, cached bool
		account        string
		resolved       bool
	}{
		{"displayed three final two later two", 2, true, false, journalAccount, false},
		{"final two later one", 1, true, false, journalAccount, true},
		{"explicit refusal", 2, false, false, journalAccount, true},
		{"cached decrease newer ordinary usage", 1, true, true, journalAccount, false},
		{"another account", 1, false, false, otherAccount, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			body := fmt.Sprintf(`{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":3,"resets_left":%d,"paused":false,"usable_now":%t}]}}`, tc.count, tc.usable)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/usage" || r.URL.RawQuery != "cedar_ember=1&skip_spend=1" {
					t.Error("only the read-only bank status GET is permitted")
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				gets.Add(1)
				writeResponse(t, w, body)
			}))
			defer server.Close()
			c := New(Options{UsageURL: server.URL + "/usage", APIBaseURL: server.URL, HTTPClient: server.Client()})
			seedPendingJournal(t, c, journalAccount, 2) // Final server baseline, not displayed 3.
			c.bank = bankCache{token: "fixture", accountID: tc.account}
			if tc.cached {
				c.bank.status = parseBankStatus([]byte(body))
				c.bank.attemptedAt, c.bank.goodAt = time.Now(), time.Now()
			}
			data := usage.UsageData{ProviderName: "Claude", FetchStatus: &usage.FetchStatus{FetchedAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}}
			c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data)
			if _, blocked := c.ResetAttemptStatus(journalNextID); blocked == tc.resolved {
				t.Fatalf("blocked=%t, want resolved=%t", blocked, tc.resolved)
			}
			ids := encodedEvidence(t, data)
			if tc.resolved && (len(ids) != 1 || ids[0] != journalFixtureID) || !tc.resolved && len(ids) != 0 {
				t.Fatalf("reconciled IDs=%v, resolved=%t", ids, tc.resolved)
			}
			wantGets := int32(1)
			if tc.cached {
				wantGets = 0
			}
			if gets.Load() != wantGets {
				t.Fatalf("status GETs=%d, want %d", gets.Load(), wantGets)
			}
			// A fresh client must recover the same pending gate/tombstone.
			restarted := New(Options{ResetStateDir: c.resetStateDir})
			if known, _ := restarted.ResetAttemptStatus(journalFixtureID); !known {
				t.Fatal("request tombstone lost on restart")
			}
			if _, blocked := restarted.ResetAttemptStatus(journalNextID); blocked == tc.resolved {
				t.Fatal("restart changed pending gate")
			}
		})
	}
}

func Test_ResetJournal_strict_whole_document_validation(t *testing.T) {
	data, _ := json.Marshal(pendingJournal(journalAccount, 2))
	valid := string(data)
	record, _ := json.Marshal(pendingJournal(journalAccount, 2).Attempts[journalFixtureID])
	for _, bad := range []string{
		`{}`, `null`, valid + `{}`, strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		fmt.Sprintf(`{"version":1,"attempts":{"%s":%s,"%s":%s}}`, journalFixtureID, record, journalFixtureID, record),
		strings.Replace(valid, `"phase":"pending"`, `"phase":"pending","phase":"reconciled"`, 1),
		strings.Replace(valid, journalFixtureID, strings.ToUpper(journalFixtureID), 1),
		strings.Replace(valid, `"available_before":2`, `"available_before":null`, 1),
		strings.Replace(valid, `"available_before":2`, `"available_before":-1`, 1),
		strings.Replace(valid, `"available_before":2`, `"available_before":9223372036854775808`, 1),
		strings.Replace(valid, `"phase":"pending"`, `"phase":"unknown"`, 1),
		strings.Replace(valid, `"phase":"pending"`, `"phase":"pending","outcome":"unknown"`, 1),
		strings.Replace(valid, `"phase":"pending"`, `"phase":"pending","extra":true`, 1),
		strings.Replace(valid, `"attempts":{`, `"extra":1,"attempts":{`, 1),
	} {
		if _, err := decodeResetJournal([]byte(bad)); err == nil {
			t.Fatalf("accepted malformed journal: %s", bad)
		}
	}
	if _, err := decodeResetJournal(data); err != nil {
		t.Fatal("valid journal rejected", err)
	}
}

func Test_ResetJournal_terminal_outcomes_do_not_bypass_fresh_status(t *testing.T) {
	for _, outcome := range []string{"", "reset", "already_redeemed", "nothing_to_reset", "unavailable"} {
		c := New(Options{})
		journal := pendingJournal(journalAccount, 2)
		attempt := journal.Attempts[journalFixtureID]
		attempt.Outcome = outcome
		journal.Attempts[journalFixtureID] = attempt
		seedJournal(t, c, journal)
		restarted := New(Options{ResetStateDir: c.resetStateDir})
		if known, _ := restarted.ResetAttemptStatus(journalFixtureID); !known {
			t.Fatal("terminal UUID lost")
		}
		if _, blocked := restarted.ResetAttemptStatus(journalNextID); !blocked {
			t.Fatalf("outcome %q released pending gate", outcome)
		}
		if len(restarted.reconciledResetRequests(attempt.AccountFingerprint)) != 0 {
			t.Fatal("terminal outcome exposed reconciliation without fresh status")
		}
	}
}

func Test_ResetJournal_missing_corrupt_or_dirty_state_revokes_cached_evidence(t *testing.T) {
	for _, damage := range []string{"missing ledger", "corrupt ledger", "dirty transaction", "namespace replaced by file"} {
		t.Run(damage, func(t *testing.T) {
			c := New(Options{})
			journal := pendingJournal(journalAccount, 2)
			attempt := journal.Attempts[journalFixtureID]
			attempt.Phase = "reconciled"
			journal.Attempts[journalFixtureID] = attempt
			seedJournal(t, c, journal)
			fingerprint := *accountFingerprint(journalAccount)
			data := usage.UsageData{RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 1, AccountFingerprint: &fingerprint,
				ReconciledRequests: func() []string { return c.reconciledResetRequests(fingerprint) }}}
			if len(encodedEvidence(t, data)) != 1 {
				t.Fatal("seeded evidence missing")
			}
			path := filepath.Join(c.resetStateDir, resetJournalName)
			switch damage {
			case "missing ledger":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt ledger":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dirty transaction":
				if err := os.WriteFile(filepath.Join(c.resetStateDir, resetJournalDirty), []byte("1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "namespace replaced by file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(c.resetStateDir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.resetStateDir, []byte("marker"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if len(encodedEvidence(t, data)) != 0 {
				t.Fatal("cached snapshot retained evidence after damage")
			}
			restarted := New(Options{ResetStateDir: c.resetStateDir})
			if known, blocked := restarted.ResetAttemptStatus(journalNextID); known || !blocked {
				t.Fatal("damaged existing namespace became spendable after restart")
			}
			if damage == "missing ledger" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("missing ledger was recreated")
				}
			}
		})
	}
	if known, blocked := New(Options{}).ResetAttemptStatus(journalNextID); known || !blocked {
		t.Fatal("unconfigured persistent state must disable consumption")
	}
}

func Test_ResetJournal_failed_resolution_retains_pending_and_shared_marker(t *testing.T) {
	journal := pendingJournal(journalAccount, 2)
	usable := false
	credits := &usage.RateLimitResetCredits{AvailableCount: 1, UsableNow: &usable}
	if err := reconcileResetJournal(&journal, credits, *accountFingerprint(journalAccount), func(*resetJournal) error { return errors.New("injected durable write failure") }); err == nil {
		t.Fatal("resolution write failure ignored")
	}
	if !journal.pending() {
		t.Fatal("failed commit leaked reconciliation into memory")
	}
	// Seed the disk shape left by a replacement whose directory sync failed:
	// visible reconciled bytes plus the marker established before that write.
	c := New(Options{})
	attempt := journal.Attempts[journalFixtureID]
	attempt.Phase = "reconciled"
	journal.Attempts[journalFixtureID] = attempt
	seedJournal(t, c, journal)
	if err := os.WriteFile(filepath.Join(c.resetStateDir, resetJournalDirty), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{ResetStateDir: c.resetStateDir})
	if len(restarted.reconciledResetRequests(*accountFingerprint(journalAccount))) != 0 {
		t.Fatal("failed durable save exposed evidence in another process")
	}
	if _, blocked := restarted.ResetAttemptStatus(journalNextID); !blocked {
		t.Fatal("failed durable save allowed admission")
	}
}

func Test_ResetJournal_keeps_all_UUID_tombstones_and_account_binding(t *testing.T) {
	c := New(Options{})
	journal := pendingJournal(journalAccount, 2)
	attempt := journal.Attempts[journalFixtureID]
	attempt.Phase, attempt.Outcome = "reconciled", "reset"
	journal.Attempts[journalFixtureID] = attempt
	for i := 1; i < maxRememberedResetAttempts; i++ {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		journal.Attempts[id] = attempt
	}
	seedJournal(t, c, journal)
	if known, blocked := c.ResetAttemptStatus(journalFixtureID); !known || blocked {
		t.Fatal("old UUID was evicted")
	}
	if _, blocked := c.ResetAttemptStatus(journalNextID); !blocked {
		t.Fatal("full journal admitted a new UUID")
	}
	ids := c.reconciledResetRequests(attempt.AccountFingerprint)
	if len(ids) != maxRememberedResetAttempts {
		t.Fatalf("evidence count=%d", len(ids))
	}
	if len(c.reconciledResetRequests(strings.Repeat("f", 64))) != 0 {
		t.Fatal("another account received evidence")
	}
	journal.Attempts[journalNextID] = attempt
	if validateResetJournal(&journal) == nil {
		t.Fatal("oversized journal accepted")
	}
}

func Test_ResetJournal_bank_GET_waits_for_shared_transaction_lock(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "cedar_ember=1&skip_spend=1" {
			t.Error("only a read-only bank GET is permitted")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		called <- struct{}{}
		writeResponse(t, w, `{"cedar_ember":{"eligible":true,"grants":[]}}`)
	}))
	defer server.Close()
	c := New(Options{UsageURL: server.URL, HTTPClient: server.Client()})
	seedPendingJournal(t, c, journalAccount, 2)
	c.bank = bankCache{token: "fixture", accountID: journalAccount}
	lock, err := credstore.LockFile(context.Background(), c.resetStateDir+".lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		data := usage.UsageData{}
		c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data)
	}()
	select {
	case <-called:
		_ = lock.Close()
		t.Fatal("bank GET started before the shared transaction finished")
	case <-time.After(40 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("read-only reconciliation did not finish")
	}
	if _, blocked := c.ResetAttemptStatus(journalNextID); blocked {
		t.Fatal("fresh post-lock status did not reconcile")
	}
}
