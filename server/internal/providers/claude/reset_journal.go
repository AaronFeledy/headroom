package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const resetJournalName = "attempts.json"
const resetJournalDirty = "write-in-progress"
const maxResetJournalBytes = 512 << 10

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type resetAttempt struct {
	AccountFingerprint string `json:"account_fingerprint"`
	AvailableBefore    int64  `json:"available_before"`
	Phase              string `json:"phase"`
	Outcome            string `json:"outcome,omitempty"`
}

type resetJournal struct {
	Version  int                     `json:"version"`
	Attempts map[string]resetAttempt `json:"attempts"`
}

func validRequestID(id string) bool {
	return uuidPattern.MatchString(id) && id == strings.ToLower(id)
}

// The directory itself is the initialization marker. Once it exists, a missing
// or damaged ledger is never recreated. A crash during first initialization
// therefore disables spending, rather than forgetting an uncertain attempt.
// All callers use one stable lock outside the namespace, including first use.
func openResetJournal(ctx context.Context, dir string, initialize bool) (journal *resetJournal, lock io.Closer, err error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, nil, errResetUnavailable
	}
	dir = filepath.Clean(dir)
	if initialize {
		if err := credstore.EnsureDirectory(ctx, filepath.Dir(dir), 0o700); err != nil {
			return nil, nil, errResetUnavailable
		}
	}
	lock, err = credstore.LockFile(ctx, dir+".lock")
	if err != nil {
		return nil, nil, errResetUnavailable
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Close())
			lock = nil
		}
	}()
	info, statErr := os.Lstat(dir)
	if os.IsNotExist(statErr) && initialize {
		if err = credstore.CreateDirectory(ctx, dir, 0o700); err != nil {
			return nil, lock, errResetUnavailable
		}
		journal = &resetJournal{Version: 1, Attempts: make(map[string]resetAttempt)}
		if err = saveResetJournal(dir, journal); err != nil {
			return nil, lock, errResetUnavailable
		}
		return journal, lock, nil
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, lock, errResetUnavailable
	}
	// Never recover a transaction whose durable commit was uncertain. This
	// marker is shared by all processes and survives server restarts.
	if _, markerErr := os.Lstat(filepath.Join(dir, resetJournalDirty)); !os.IsNotExist(markerErr) {
		return nil, lock, errResetUnavailable
	}
	path := filepath.Join(dir, resetJournalName)
	info, err = os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxResetJournalBytes {
		return nil, lock, errResetUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxResetJournalBytes {
		return nil, lock, errResetUnavailable
	}
	journal, err = decodeResetJournal(data)
	return journal, lock, err
}

// Reject duplicate keys and nulls before decoding. encoding/json otherwise
// silently accepts duplicate UUIDs/fields and null numeric/string fields.
func uniqueJournalValue(decoder *json.Decoder, depth int) error {
	if depth > 4 {
		return errResetUnavailable
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return errResetUnavailable
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if delim != '{' {
		return errResetUnavailable
	}
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return errResetUnavailable
		}
		switch depth {
		case 0:
			if name != "version" && name != "attempts" {
				return errResetUnavailable
			}
		case 1:
			if !validRequestID(name) {
				return errResetUnavailable
			}
		case 2:
			switch name {
			case "account_fingerprint", "available_before", "phase", "outcome":
			default:
				return errResetUnavailable
			}
		default:
			return errResetUnavailable
		}
		seen[name] = true
		if err := uniqueJournalValue(decoder, depth+1); err != nil {
			return err
		}
	}
	// Zero is meaningful for a not-submitted tombstone, so it must never be
	// supplied implicitly by a missing baseline field.
	if depth == 2 && !seen["available_before"] {
		return errResetUnavailable
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return errResetUnavailable
	}
	return nil
}

func decodeResetJournal(data []byte) (*resetJournal, error) {
	duplicates := json.NewDecoder(bytes.NewReader(data))
	if uniqueJournalValue(duplicates, 0) != nil {
		return nil, errResetUnavailable
	}
	if _, err := duplicates.Token(); err != io.EOF {
		return nil, errResetUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal resetJournal
	if decoder.Decode(&journal) != nil || validateResetJournal(&journal) != nil {
		return nil, errResetUnavailable
	}
	return &journal, nil
}

func validateResetJournal(journal *resetJournal) error {
	if journal.Version != 1 || journal.Attempts == nil || len(journal.Attempts) > maxRememberedResetAttempts {
		return errResetUnavailable
	}
	pending := make(map[string]bool)
	for id, attempt := range journal.Attempts {
		if !validRequestID(id) || !fingerprintPattern.MatchString(attempt.AccountFingerprint) {
			return errResetUnavailable
		}
		switch attempt.Outcome {
		case "", "reset", "already_redeemed", "nothing_to_reset", "unavailable":
		default:
			return errResetUnavailable
		}
		switch attempt.Phase {
		case "pending":
			if attempt.AvailableBefore <= 0 {
				return errResetUnavailable
			}
			if pending[attempt.AccountFingerprint] {
				return errResetUnavailable
			}
			pending[attempt.AccountFingerprint] = true
		case "reconciled":
			if attempt.AvailableBefore <= 0 {
				return errResetUnavailable
			}
		case "not_submitted":
			if attempt.AvailableBefore != 0 || attempt.Outcome != "unavailable" {
				return errResetUnavailable
			}
		default:
			return errResetUnavailable
		}
	}
	return nil
}

func saveResetJournal(dir string, journal *resetJournal) error {
	if err := validateResetJournal(journal); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return errResetUnavailable
	}
	marker := filepath.Join(dir, resetJournalDirty)
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errResetUnavailable
	}
	_, writeErr := file.WriteString("1\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := credstore.SyncDirectory(dir); err != nil {
		return err
	}
	// Also persist the marker with the platform's atomic replacement primitive.
	// On Windows this uses MoveFileEx WRITE_THROUGH, where directory sync alone
	// is unavailable. The exclusive creation above still rejects an old marker.
	if err := credstore.WriteFileAtomically(marker, []byte("1\n"), 0o600); err != nil {
		return err
	}
	if err := credstore.WriteFileAtomically(filepath.Join(dir, resetJournalName), data, 0o600); err != nil {
		return err // Keep the marker even if rename succeeded but directory sync failed.
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	// No post-unlink sync is needed for safety: a crash can restore the marker
	// and disable resets, but cannot expose an uncommitted ledger.
	return nil
}

func (journal *resetJournal) pending() bool {
	for _, attempt := range journal.Attempts {
		if attempt.Phase == "pending" {
			return true
		}
	}
	return false
}

// This transition only records that a validated request was rejected before
// submission. An existing entry may belong to an uncertain attempt in another
// process: never replace it, even when its account matches this request.
// Call only under resetMu -> bankMu -> journal lock; publish a copy after commit.
func recordResetNotSubmitted(journal *resetJournal, requestID, fingerprint string, commit func(*resetJournal) error) error {
	if !validRequestID(requestID) || !fingerprintPattern.MatchString(fingerprint) || validateResetJournal(journal) != nil {
		return errResetUnavailable
	}
	if _, exists := journal.Attempts[requestID]; exists {
		return nil
	}
	if len(journal.Attempts) >= maxRememberedResetAttempts {
		return errResetUnavailable
	}
	updated := resetJournal{Version: journal.Version, Attempts: make(map[string]resetAttempt, len(journal.Attempts)+1)}
	for id, attempt := range journal.Attempts {
		updated.Attempts[id] = attempt
	}
	updated.Attempts[requestID] = resetAttempt{AccountFingerprint: fingerprint, Phase: "not_submitted", Outcome: "unavailable"}
	if err := commit(&updated); err != nil {
		return err
	}
	*journal = updated
	return nil
}

// RecordResetNotSubmitted has no provider or credential access. It can finalize
// a validated rejection only after all possible submission code has returned.
// Missing or damaged existing state must never become evidence of non-submission.
func (c *Client) RecordResetNotSubmitted(ctx context.Context, requestID, fingerprint string) error {
	if lockResetRejection(ctx, &c.resetMu) != nil {
		return errResetUnavailable
	}
	defer c.resetMu.Unlock()
	if lockResetRejection(ctx, &c.bankMu) != nil {
		return errResetUnavailable
	}
	defer c.bankMu.Unlock()
	return c.recordResetNotSubmittedLocked(ctx, requestID, fingerprint)
}

// Storage-only rejection transition, under resetMu -> bankMu. Keeping the
// guarded first-use open here lets isolated storage tests cover the same path.
func (c *Client) recordResetNotSubmittedLocked(ctx context.Context, requestID, fingerprint string) error {
	// A validated rejection can be the first Claude reset operation when a
	// different provider owns the shared handler lock. Create only a never-used
	// namespace; openResetJournal rejects missing or damaged existing ledgers.
	journal, lock, err := c.openResetJournal(ctx, true)
	if err != nil {
		return errResetUnavailable
	}
	if ctx.Err() != nil {
		err = errResetUnavailable
	} else {
		err = recordResetNotSubmitted(journal, requestID, fingerprint, c.commitResetJournal)
	}
	if closeErr := lock.Close(); closeErr != nil {
		c.resetJournalFailed.Store(true)
		return errResetUnavailable
	}
	return err
}

// Wait in this caller, never in a background worker. HTTP rejection finalization
// uses Background so healthy contention cannot abandon its durable evidence.
// Other callers can still cancel while waiting; no work continues after return.
func lockResetRejection(ctx context.Context, mutex *sync.Mutex) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return errResetUnavailable
		}
		if mutex.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return errResetUnavailable
		case <-ticker.C:
		}
	}
}

// Pure replay policy: every remembered UUID is terminal for submission. A
// not-submitted UUID can never be promoted into a new pending attempt.
func resetAttemptResult(attempt resetAttempt) (outcome string, ambiguous bool, err error) {
	if attempt.Phase == "not_submitted" {
		return "unavailable", false, nil
	}
	if attempt.Outcome == "" {
		return "", true, errResetUnavailable
	}
	if attempt.Outcome == "reset" {
		return "already_redeemed", false, nil
	}
	return attempt.Outcome, false, nil
}

// Only call under resetMu -> bankMu -> journal lock, after a successful fresh
// bank GET made while holding those locks. No clock or cached usage can prove
// causality. Change a copy and publish only after the durable commit succeeds.
func reconcileResetJournal(journal *resetJournal, credits *usage.RateLimitResetCredits, fingerprint string, commit func(*resetJournal) error) error {
	if credits == nil || !fingerprintPattern.MatchString(fingerprint) {
		return nil
	}
	updated := resetJournal{Version: journal.Version, Attempts: make(map[string]resetAttempt, len(journal.Attempts))}
	changed := false
	for id, attempt := range journal.Attempts {
		if attempt.Phase == "pending" && attempt.AccountFingerprint == fingerprint &&
			(credits.AvailableCount < attempt.AvailableBefore || credits.UsableNow != nil && !*credits.UsableNow) {
			attempt.Phase = "reconciled"
			changed = true
		}
		updated.Attempts[id] = attempt
	}
	if !changed {
		return nil
	}
	if err := commit(&updated); err != nil {
		return err
	}
	*journal = updated
	return nil
}

// Re-read on every usage serialization, even when the provider snapshot is
// cached. Missing/corrupt disk state must revoke previously exposed evidence.
func (c *Client) reconciledResetRequests(fingerprint string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	journal, lock, err := c.openResetJournal(ctx, false)
	if err != nil {
		return nil
	}
	var ids []string
	for id, attempt := range journal.Attempts {
		if (attempt.Phase == "reconciled" || attempt.Phase == "not_submitted") && attempt.AccountFingerprint == fingerprint {
			ids = append(ids, id)
		}
	}
	if lock.Close() != nil {
		return nil
	}
	sort.Strings(ids)
	return ids
}

func (c *Client) openResetJournal(ctx context.Context, initialize bool) (*resetJournal, io.Closer, error) {
	if c.resetJournalFailed.Load() {
		return nil, nil, errResetUnavailable
	}
	return openResetJournal(ctx, c.resetStateDir, initialize)
}

func (c *Client) commitResetJournal(journal *resetJournal) error {
	if err := saveResetJournal(c.resetStateDir, journal); err != nil {
		// A failed sync can follow a successful rename. Re-reading those visible
		// bytes must not expose unconfirmed reconciliation in this process.
		c.resetJournalFailed.Store(true)
		return err
	}
	return nil
}
