package resetauto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const (
	stateFileName = "authorizations.json"
	stateVersion  = 1
	maxStateBytes = 64 << 10
	maxArmKinds   = 8
)

var (
	// ErrStateUnavailable means the authorization file cannot be trusted. The
	// caller reports disarmed state, never fires, and never rewrites the file.
	ErrStateUnavailable = errors.New("automatic reset state unavailable")
	// ErrConflicting means a different authorization is already armed.
	ErrConflicting = errors.New("a different automatic reset is armed")
	// ErrStaleAuthorization means the authorization changed between evaluation
	// and the spend transition, so nothing may be spent.
	ErrStaleAuthorization = errors.New("automatic reset authorization changed")

	errInvalidState = errors.New("invalid automatic reset state")
)

// Store is the durable, cross-process authorization file. The file lives in an
// owner-only directory beside the Claude reset journal; a stable sibling lock
// file serializes every read-modify-write.
type Store struct {
	dir      string
	lockPath string
	now      func() time.Time
}

// NewStore accepts the configured directory. A blank or relative path disables
// the store: automation then reports nothing and refuses to arm.
func NewStore(dir string) *Store {
	if dir == "" || !filepath.IsAbs(dir) {
		return &Store{now: time.Now}
	}
	dir = filepath.Clean(dir)
	return &Store{dir: dir, lockPath: dir + ".lock", now: time.Now}
}

func (s *Store) Available() bool { return s != nil && s.dir != "" }

// Load re-reads the file for one evaluation or report. Reads are unlocked
// because replacement is atomic; every mutation re-reads under the lock.
func (s *Store) Load() (map[string]Record, error) {
	if !s.Available() {
		return nil, ErrStateUnavailable
	}
	path := filepath.Join(s.dir, stateFileName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return nil, ErrStateUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxStateBytes {
		return nil, ErrStateUnavailable
	}
	return decodeState(data)
}

// Arm stores one authorization. An identical request refreshes nothing and is
// idempotent; a different request while armed conflicts.
func (s *Store) Arm(ctx context.Context, provider string, request ArmRequest) (Record, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	records, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	existing := records[provider]
	if existing.Armed() && s.now().UTC().Before(existing.WindowEndsAt) {
		if existing.matches(request) {
			return existing, nil
		}
		return existing, ErrConflicting
	}
	record := Record{
		ArmedAt:            s.now().UTC().Truncate(time.Second),
		WindowEndsAt:       request.WindowEndsAt.UTC().Truncate(time.Second),
		Kinds:              append([]string(nil), request.Kinds...),
		AccountFingerprint: request.AccountFingerprint,
		LastEvent:          existing.LastEvent,
	}
	if provider == ProviderCodex || len(record.Kinds) == 0 {
		record.Kinds = nil
	}
	records[provider] = record
	if err := s.save(records); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Cancel cancels whatever is armed and records the caller's reason. It is
// idempotent: when nothing is armed it returns the existing record unchanged.
func (s *Store) Cancel(ctx context.Context, provider, reason string) (Record, error) {
	return s.cancel(ctx, provider, reason, nil)
}

// CancelIfEvaluation cancels only the authorization the evaluator observed.
func (s *Store) CancelIfEvaluation(ctx context.Context, provider string, record Record, reason string) (Record, error) {
	return s.cancel(ctx, provider, reason, &record)
}

func (s *Store) cancel(ctx context.Context, provider, reason string, expected *Record) (Record, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	records, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	current := records[provider]
	if !current.Armed() {
		return current, nil
	}
	if expected != nil && !current.sameAuthorization(*expected) {
		return current, nil
	}
	record := Record{LastEvent: newEvent(s.now(), "canceled", "", reason, "")}
	records[provider] = record
	if err := s.save(records); err != nil {
		return Record{}, err
	}
	return record, nil
}

// BeginFire disarms the exact authorization the evaluator observed and records
// the request before any spend. A failed write aborts the spend.
func (s *Store) BeginFire(ctx context.Context, provider string, record Record, kind string) error {
	lock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	records, err := s.loadLocked()
	if err != nil {
		return err
	}
	current := records[provider]
	if !current.Armed() || !current.sameAuthorization(record) || !s.now().UTC().Before(current.WindowEndsAt) {
		return ErrStaleAuthorization
	}
	records[provider] = Record{LastEvent: newEvent(s.now(), "requested", "", "", kind)}
	if err := s.save(records); err != nil {
		return err
	}
	return nil
}

// FinishFire records the terminal outcome of a fired authorization. The
// authorization itself is already gone and is not recreated.
func (s *Store) FinishFire(ctx context.Context, provider string, event *usage.ResetAutomationEvent) error {
	lock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	records, err := s.loadLocked()
	if err != nil {
		return err
	}
	record := records[provider]
	record.LastEvent = event
	records[provider] = record
	return s.save(records)
}

func (s *Store) lock(ctx context.Context) (io.Closer, error) {
	if !s.Available() {
		return nil, ErrStateUnavailable
	}
	if err := credstore.EnsureDirectory(ctx, filepath.Dir(s.dir), 0o700); err != nil {
		return nil, ErrStateUnavailable
	}
	if err := s.ensureDir(ctx); err != nil {
		return nil, err
	}
	lock, err := credstore.LockFile(ctx, s.lockPath)
	if err != nil {
		return nil, ErrStateUnavailable
	}
	return lock, nil
}

func (s *Store) ensureDir(ctx context.Context) error {
	info, err := os.Lstat(s.dir)
	if os.IsNotExist(err) {
		// A concurrent process may create the directory first; that is success.
		if err := credstore.CreateDirectory(ctx, s.dir, 0o700); err != nil {
			info, err = os.Lstat(s.dir)
			if err != nil {
				return ErrStateUnavailable
			}
		} else {
			return nil
		}
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStateUnavailable
	}
	return nil
}

func (s *Store) loadLocked() (map[string]Record, error) {
	return s.Load()
}

func (s *Store) save(records map[string]Record) error {
	file := stateFile{Version: stateVersion, Providers: make(map[string]storedRecord, len(records))}
	for provider, record := range records {
		stored, err := record.stored(provider)
		if err != nil {
			return ErrStateUnavailable
		}
		file.Providers[provider] = stored
	}
	data, err := json.Marshal(file)
	if err != nil || len(data) > maxStateBytes {
		return ErrStateUnavailable
	}
	if err := credstore.WriteFileAtomically(filepath.Join(s.dir, stateFileName), data, 0o600); err != nil {
		return ErrStateUnavailable
	}
	return nil
}

func (r Record) matches(request ArmRequest) bool {
	return r.AccountFingerprint == request.AccountFingerprint &&
		sameInstant(r.WindowEndsAt, request.WindowEndsAt) &&
		sameKinds(r.Kinds, request.Kinds)
}

func (r Record) sameAuthorization(other Record) bool {
	return r.ArmedAt.Equal(other.ArmedAt) && r.AccountFingerprint == other.AccountFingerprint
}

func sameKinds(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
