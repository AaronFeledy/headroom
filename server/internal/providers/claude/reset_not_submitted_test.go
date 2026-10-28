package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// Only pure transitions, seeded storage, policy queries, and serialization run
// here. Never call the production finalizer, consumer, preparation, or routes.
func notSubmittedJournal(fingerprint string) resetJournal {
	return resetJournal{Version: 1, Attempts: map[string]resetAttempt{journalFixtureID: {
		AccountFingerprint: fingerprint, Phase: "not_submitted", Outcome: "unavailable",
	}}}
}

func recordRejectionStorageFixture(c *Client, requestID, fingerprint string) error {
	c.resetMu.Lock()
	defer c.resetMu.Unlock()
	c.bankMu.Lock()
	defer c.bankMu.Unlock()
	return c.recordResetNotSubmittedLocked(context.Background(), requestID, fingerprint)
}

func Test_ResetJournal_rejected_UUID_is_durable_account_bound_evidence(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("other uncertain UUID=%t", uncertain), func(t *testing.T) {
			c := New(Options{})
			initial := resetJournal{Version: 1, Attempts: make(map[string]resetAttempt)}
			if uncertain {
				initial = pendingJournal(journalAccount, 2)
			}
			seedJournal(t, c, initial)
			journal, lock, err := openResetJournal(context.Background(), c.resetStateDir, false)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise only the storage transition with a real durable commit.
			err = recordResetNotSubmitted(journal, journalNextID, fingerprint, c.commitResetJournal)
			closeErr := lock.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("storage transition=%v close=%v", err, closeErr)
			}
			want := resetAttempt{AccountFingerprint: fingerprint, Phase: "not_submitted", Outcome: "unavailable"}
			if journal.Attempts[journalNextID] != want || journal.pending() != uncertain {
				t.Fatal("rejection did not preserve the unrelated uncertain request")
			}
			restarted := New(Options{ResetStateDir: c.resetStateDir})
			if known, blocked := restarted.ResetAttemptStatus(journalNextID); !known || blocked {
				t.Fatal("rejected UUID did not remain known after restart")
			}
			data := usage.UsageData{RateLimitResetCredits: &usage.RateLimitResetCredits{
				AvailableCount: 2, AccountFingerprint: &fingerprint,
				ReconciledRequests: func() []string { return restarted.reconciledResetRequests(fingerprint) },
			}}
			if ids := encodedEvidence(t, data); len(ids) != 1 || ids[0] != journalNextID {
				t.Fatalf("rejected UUID evidence=%v", ids)
			}
			if len(restarted.reconciledResetRequests(strings.Repeat("f", 64))) != 0 {
				t.Fatal("rejection evidence leaked to another account")
			}
			if uncertain {
				if known, blocked := restarted.ResetAttemptStatus("cccccccc-cccc-4ccc-8ccc-cccccccccccc"); known || !blocked {
					t.Fatal("a rejected new UUID released an existing uncertain gate")
				}
			}
		})
	}
}

func Test_ResetJournal_first_rejection_uses_guarded_initialization(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	for _, statusFirst := range []bool{false, true} {
		name := "rejection first"
		if statusFirst {
			name = "status first"
		}
		t.Run(name, func(t *testing.T) {
			// Model a Claude UUID rejected while another provider holds the
			// handler lock, before any Claude status or journal access. Only
			// storage helpers and read-only status are used here.
			dir := filepath.Join(t.TempDir(), "state")
			c := New(Options{ResetStateDir: dir})
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatalf("expected virgin namespace, stat=%v", err)
			}
			if statusFirst {
				if known, blocked := c.ResetAttemptStatus(journalFixtureID); known || blocked {
					t.Fatal("first read-only status did not initialize an empty journal")
				}
			}
			if err := recordRejectionStorageFixture(c, journalNextID, fingerprint); err != nil {
				t.Fatalf("first rejection storage transition: %v", err)
			}
			restarted := New(Options{ResetStateDir: dir})
			if ids := restarted.reconciledResetRequests(fingerprint); !reflect.DeepEqual(ids, []string{journalNextID}) {
				t.Fatalf("first rejection evidence lost on restart: %v", ids)
			}
			if known, blocked := restarted.ResetAttemptStatus(journalNextID); !known || blocked {
				t.Fatal("rejected UUID was not terminal after restart")
			}
			if known, blocked := restarted.ResetAttemptStatus(journalFixtureID); known || blocked {
				t.Fatal("unrelated UUID became blocked or known")
			}
		})
	}
}

func Test_ResetJournal_rejection_initialization_preserves_damaged_existing_state(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	for _, damage := range []string{"missing ledger", "corrupt ledger", "dirty transaction"} {
		t.Run(damage, func(t *testing.T) {
			c := New(Options{ResetStateDir: filepath.Join(t.TempDir(), "state")})
			seedJournal(t, c, pendingJournal(journalAccount, 2))
			path := filepath.Join(c.resetStateDir, resetJournalName)
			var err error
			switch damage {
			case "missing ledger":
				err = os.Remove(path)
			case "corrupt ledger":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "dirty transaction":
				err = os.WriteFile(filepath.Join(c.resetStateDir, resetJournalDirty), []byte("1\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := recordRejectionStorageFixture(c, journalNextID, fingerprint); err == nil {
				t.Fatal("guarded initialization accepted damaged existing state")
			}
			restarted := New(Options{ResetStateDir: c.resetStateDir})
			if ids := restarted.reconciledResetRequests(fingerprint); len(ids) != 0 {
				t.Fatalf("damage exposed false evidence: %v", ids)
			}
			if known, blocked := restarted.ResetAttemptStatus(journalNextID); known || !blocked {
				t.Fatal("damaged namespace became spendable")
			}
			if damage == "missing ledger" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("missing ledger was recreated")
				}
			}
		})
	}
}

func Test_ResetJournal_rejection_waits_for_healthy_contention_then_survives_restart(t *testing.T) {
	// Exercise the lock-only helper and storage transition, never the production
	// finalizer, consumer, preparation, arming, or a reset route.
	c := New(Options{})
	seedPendingJournal(t, c, journalAccount, 2)
	fingerprint := *accountFingerprint(journalAccount)
	c.resetMu.Lock()
	release := sync.OnceFunc(c.resetMu.Unlock)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("fixture worker did not release its locks during cleanup")
		}
	})
	go func() {
		defer close(finished)
		close(started)
		if err := lockResetMutex(context.Background(), &c.resetMu); err != nil {
			done <- err
			return
		}
		defer c.resetMu.Unlock()
		// Bound the remaining fixture lock waits and cancel them during cleanup.
		storageCtx, storageCancel := context.WithTimeout(ctx, 3*time.Second)
		defer storageCancel()
		if err := lockResetMutex(storageCtx, &c.bankMu); err != nil {
			done <- err
			return
		}
		defer c.bankMu.Unlock()
		journal, lock, err := openResetJournal(storageCtx, c.resetStateDir, false)
		if err != nil {
			done <- err
			return
		}
		err = recordResetNotSubmitted(journal, journalNextID, fingerprint, c.commitResetJournal)
		done <- errors.Join(err, lock.Close())
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture worker did not start")
	}
	// Keep the earlier operation's lock beyond the former one-second budget.
	select {
	case err := <-done:
		t.Fatalf("rejection stopped waiting while the earlier operation held its lock: %v", err)
	case <-time.After(1100 * time.Millisecond):
	}
	if ids := c.reconciledResetRequests(fingerprint); len(ids) != 0 {
		t.Errorf("rejection exposed evidence before acquiring the transaction locks: %v", ids)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rejection storage transition did not finish after fixture lock release")
	}
	restarted := New(Options{ResetStateDir: c.resetStateDir})
	if ids := restarted.reconciledResetRequests(fingerprint); !reflect.DeepEqual(ids, []string{journalNextID}) {
		t.Fatalf("queued rejection evidence lost after restart: %v", ids)
	}
	journal, lock, err := openResetJournal(context.Background(), c.resetStateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := lock.Close()
	if closeErr != nil || journal.Attempts[journalFixtureID] != pendingJournal(journalAccount, 2).Attempts[journalFixtureID] || !journal.pending() {
		t.Fatalf("queued rejection changed the unrelated pending attempt: close=%v", closeErr)
	}
}

func Test_ResetJournal_status_deadline_covers_finalizer_mutex_wait(t *testing.T) {
	// Seeded storage and a held mutex only: no consumer, preparation, arming,
	// production finalizer, or reset route is invoked.
	c := New(Options{})
	seedPendingJournal(t, c, journalAccount, 2)
	c.resetMu.Lock()
	release := sync.OnceFunc(c.resetMu.Unlock)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	type status struct{ known, blocked bool }
	done := make(chan status, 1)
	go func() {
		known, blocked := c.resetAttemptStatus(ctx, journalFixtureID)
		done <- status{known, blocked}
	}()
	select {
	case got := <-done:
		if got.known || !got.blocked {
			t.Fatalf("status behind a held finalizer mutex = (%t, %t), want (false, true)", got.known, got.blocked)
		}
	case <-time.After(time.Second):
		release() // Let a regressed blocking read exit before failing.
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("status mutex wait exceeded its deadline")
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if known, blocked := c.resetAttemptStatus(ctx, journalFixtureID); !known || blocked {
		t.Fatalf("seeded known status = (%t, %t), want (true, false)", known, blocked)
	}
}

func Test_ResetJournal_rejection_never_replaces_any_existing_UUID(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	for _, phase := range []string{"pending", "reconciled", "not_submitted"} {
		for _, sameAccount := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same account=%t", phase, sameAccount), func(t *testing.T) {
				journal := pendingJournal(journalAccount, 2)
				original := journal.Attempts[journalFixtureID]
				original.Phase = phase
				if phase == "not_submitted" {
					original.AvailableBefore, original.Outcome = 0, "unavailable"
				}
				journal.Attempts[journalFixtureID] = original
				requestFingerprint := fingerprint
				if !sameAccount {
					requestFingerprint = strings.Repeat("f", 64)
				}
				commits := 0
				if err := recordResetNotSubmitted(&journal, journalFixtureID, requestFingerprint, func(*resetJournal) error {
					commits++
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if commits != 0 || len(journal.Attempts) != 1 || journal.Attempts[journalFixtureID] != original {
					t.Fatal("existing UUID was rewritten or recommitted")
				}
			})
		}
	}
}

func Test_ResetJournal_not_submitted_schema_requires_exact_zero_baseline(t *testing.T) {
	data, err := json.Marshal(notSubmittedJournal(*accountFingerprint(journalAccount)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeResetJournal(data); err != nil {
		t.Fatal("valid not-submitted tombstone rejected", err)
	}
	valid := string(data)
	for _, bad := range []string{
		strings.Replace(valid, `"available_before":0,`, "", 1),
		strings.Replace(valid, `"available_before":0`, `"available_before":null`, 1),
		strings.Replace(valid, `"available_before":0`, `"available_before":1`, 1),
		strings.Replace(valid, `"available_before":0`, `"available_before":-1`, 1),
		strings.Replace(valid, `"outcome":"unavailable"`, `"outcome":"reset"`, 1),
		strings.Replace(valid, `,"outcome":"unavailable"`, "", 1),
		strings.Replace(valid, `"phase":"not_submitted"`, `"phase":"pending"`, 1),
		strings.Replace(valid, `"phase":"not_submitted"`, `"phase":"reconciled"`, 1),
	} {
		if _, err := decodeResetJournal([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid not-submitted tombstone: %s", bad)
		}
	}
}

func Test_ResetJournal_failed_rejection_commit_publishes_no_evidence(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	journal := pendingJournal(journalAccount, 2)
	before, _ := json.Marshal(journal)
	err := recordResetNotSubmitted(&journal, journalNextID, fingerprint, func(updated *resetJournal) error {
		if updated.Attempts[journalNextID].Phase != "not_submitted" {
			t.Fatal("commit did not receive the proposed tombstone")
		}
		return errors.New("injected durable write failure")
	})
	after, _ := json.Marshal(journal)
	if err == nil || string(before) != string(after) {
		t.Fatal("failed commit changed the journal in memory")
	}
	// A marker already present makes the actual durable storage writer fail.
	c := New(Options{})
	seedJournal(t, c, journal)
	if err := os.WriteFile(filepath.Join(c.resetStateDir, resetJournalDirty), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordResetNotSubmitted(&journal, journalNextID, fingerprint, c.commitResetJournal); err == nil {
		t.Fatal("failed storage commit ignored")
	}
	if _, exists := journal.Attempts[journalNextID]; exists || len(c.reconciledResetRequests(fingerprint)) != 0 {
		t.Fatal("failed storage commit exposed a rejected UUID")
	}
	// A replacement may be visible before a failed sync. The shared marker
	// must hide even those bytes from a restarted reader.
	seedJournal(t, c, notSubmittedJournal(fingerprint))
	restarted := New(Options{ResetStateDir: c.resetStateDir})
	if len(restarted.reconciledResetRequests(fingerprint)) != 0 {
		t.Fatal("restart exposed an uncommitted tombstone")
	}
	if known, blocked := restarted.ResetAttemptStatus(journalNextID); known || !blocked {
		t.Fatal("restart admitted a UUID after uncertain storage commit")
	}
}

func Test_ResetJournal_rejection_keeps_capacity_and_validation_gates(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	journal := notSubmittedJournal(fingerprint)
	attempt := journal.Attempts[journalFixtureID]
	for i := 1; i < maxRememberedResetAttempts; i++ {
		journal.Attempts[fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)] = attempt
	}
	commits := 0
	commit := func(*resetJournal) error { commits++; return nil }
	if recordResetNotSubmitted(&journal, journalNextID, fingerprint, commit) == nil || commits != 0 {
		t.Fatal("full journal admitted a rejected new UUID")
	}
	if recordResetNotSubmitted(&journal, journalFixtureID, fingerprint, commit) != nil || commits != 0 {
		t.Fatal("exact existing tombstone was not idempotent at capacity")
	}
	for _, tc := range []struct{ id, fingerprint string }{
		{"invalid", fingerprint}, {strings.ToUpper(journalFixtureID), fingerprint},
		{journalNextID, ""}, {journalNextID, strings.ToUpper(fingerprint)},
	} {
		empty := resetJournal{Version: 1, Attempts: make(map[string]resetAttempt)}
		if recordResetNotSubmitted(&empty, tc.id, tc.fingerprint, commit) == nil || len(empty.Attempts) != 0 || commits != 0 {
			t.Fatal("invalid rejection identity was accepted")
		}
	}
}

func Test_ResetJournal_not_submitted_replay_is_terminal_policy(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	journal := notSubmittedJournal(fingerprint)
	before := journal.Attempts[journalFixtureID]
	for i := 0; i < 3; i++ {
		outcome, ambiguous, err := resetAttemptResult(journal.Attempts[journalFixtureID])
		if outcome != "unavailable" || ambiguous || err != nil || journal.Attempts[journalFixtureID] != before || journal.pending() {
			t.Fatal("remembered rejection became retryable")
		}
	}
	uncertain := pendingJournal(journalAccount, 2).Attempts[journalFixtureID]
	if outcome, ambiguous, err := resetAttemptResult(uncertain); outcome != "" || !ambiguous || err == nil {
		t.Fatal("existing uncertain attempt lost its replay gate")
	}
}

func Test_ResetJournal_not_submitted_damage_revokes_cached_evidence(t *testing.T) {
	fingerprint := *accountFingerprint(journalAccount)
	for _, damage := range []string{"missing", "corrupt", "dirty"} {
		t.Run(damage, func(t *testing.T) {
			c := New(Options{})
			seedJournal(t, c, notSubmittedJournal(fingerprint))
			data := usage.UsageData{RateLimitResetCredits: &usage.RateLimitResetCredits{
				AccountFingerprint: &fingerprint,
				ReconciledRequests: func() []string { return c.reconciledResetRequests(fingerprint) },
			}}
			if !reflect.DeepEqual(encodedEvidence(t, data), []string{journalFixtureID}) {
				t.Fatal("seeded rejection evidence missing")
			}
			path := filepath.Join(c.resetStateDir, resetJournalName)
			var err error
			switch damage {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "dirty":
				err = os.WriteFile(filepath.Join(c.resetStateDir, resetJournalDirty), []byte("1\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(encodedEvidence(t, data)) != 0 {
				t.Fatal("cached usage retained rejection evidence after damage")
			}
			restarted := New(Options{ResetStateDir: c.resetStateDir})
			if known, blocked := restarted.ResetAttemptStatus(journalNextID); known || !blocked {
				t.Fatal("damaged state became spendable after restart")
			}
		})
	}
}
