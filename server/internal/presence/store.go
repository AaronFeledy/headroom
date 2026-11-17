// Package presence remembers which provider rows proved the user has an
// account, so a provider that lost its sign-in keeps its reconnect card after
// the server restarts. It never stores credentials, paths, or account labels.
package presence

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxRows is the durable and in-memory bound. Adding beyond it drops the
// oldest remembered row.
const maxRows = 256

// Options configures a Store. A blank or relative Dir keeps it memory-only.
type Options struct {
	Dir    string
	Logger *slog.Logger
}

// Store is the process-wide presence memory. It reads its file once at
// construction and writes only when a row is newly remembered; a directory
// that cannot be written leaves it running purely in memory.
type Store struct {
	dir    string
	logger *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	writable bool
	rows     map[string]time.Time

	// writeMu serializes read-modify-write cycles across providers. The row
	// map is never held while the file is written.
	writeMu     sync.Mutex
	writeWarned bool
}

// New loads the presence file. It never creates the directory or file; that
// happens lazily on the first Record. A newer schema or an unreadable file
// leaves the store memory-only.
func New(opts Options) *Store {
	dir := strings.TrimSpace(opts.Dir)
	if !filepath.IsAbs(dir) {
		dir = ""
	}
	store := &Store{dir: dir, logger: opts.Logger, now: time.Now, rows: make(map[string]time.Time)}
	store.load()
	return store
}

// Present reports whether the row key is remembered. It never touches disk.
func (s *Store) Present(key string) bool {
	if s == nil || !validRowKey(key) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.rows[key]
	return ok
}

// Record remembers key as first seen now and logs the first detection. New
// keys are unioned with the file on disk and written atomically; an already
// remembered key never writes.
func (s *Store) Record(key string) {
	if s == nil {
		return
	}
	key = strings.TrimSpace(key)
	if !validRowKey(key) {
		return
	}
	s.mu.Lock()
	if _, seen := s.rows[key]; seen {
		s.mu.Unlock()
		return
	}
	s.rows[key] = s.now().UTC().Truncate(time.Second)
	capRows(s.rows)
	pending := cloneRows(s.rows)
	writable := s.writable
	s.mu.Unlock()
	s.logDetected(key)
	if !writable {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.writeUnion(pending)
}

func (s *Store) load() {
	if s.dir == "" {
		return
	}
	status, rows := readSeenFile(filepath.Join(s.dir, seenFileName))
	switch status {
	case fileMissing:
		s.writable = true
	case fileLoaded:
		s.writable = true
		for key, at := range rows {
			s.rows[key] = at
		}
	case fileCorrupt:
		// Remember nothing from a damaged file; the next add replaces it.
		s.writable = true
		s.warn("provider presence state is damaged; starting with no remembered providers")
	case fileUnreadable:
		s.warn("provider presence state is unavailable; running in memory")
	}
}

// writeUnion re-reads the file, unions the rows another process may have
// added, and replaces it atomically with the capped result.
func (s *Store) writeUnion(pending map[string]time.Time) {
	path := filepath.Join(s.dir, seenFileName)
	status, disk := readSeenFile(path)
	if status == fileNewer || status == fileUnreadable {
		s.warnWrite()
		return
	}
	rows := make(map[string]time.Time, len(disk)+len(pending))
	for key, at := range disk {
		rows[key] = at
	}
	for key, at := range pending {
		if existing, ok := rows[key]; !ok || at.Before(existing) {
			rows[key] = at
		}
	}
	capRows(rows)
	if err := writeSeenFile(s.dir, path, rows); err != nil {
		s.warnWrite()
		return
	}
	s.mu.Lock()
	for key, at := range rows {
		if existing, ok := s.rows[key]; !ok || at.Before(existing) {
			s.rows[key] = at
		}
	}
	capRows(s.rows)
	s.mu.Unlock()
}

func (s *Store) warn(message string) {
	if s.logger != nil {
		s.logger.Warn(message)
	}
}

// warnWrite reports the first write failure only; callers hold writeMu.
func (s *Store) warnWrite() {
	if s.writeWarned {
		return
	}
	s.writeWarned = true
	s.warn("provider presence state could not be written; running in memory")
}

func (s *Store) logDetected(key string) {
	if s.logger == nil {
		return
	}
	provider := key
	if index := strings.IndexByte(key, ':'); index >= 0 {
		provider = key[:index]
	}
	s.logger.Info("provider account detected", "provider", provider)
}

func cloneRows(rows map[string]time.Time) map[string]time.Time {
	cloned := make(map[string]time.Time, len(rows))
	for key, at := range rows {
		cloned[key] = at
	}
	return cloned
}

// capRows drops the oldest rows until the map fits, breaking ties by key so a
// rewrite is deterministic.
func capRows(rows map[string]time.Time) {
	for len(rows) > maxRows {
		oldestKey := ""
		var oldest time.Time
		for key, at := range rows {
			if oldestKey == "" || at.Before(oldest) || (at.Equal(oldest) && key < oldestKey) {
				oldestKey, oldest = key, at
			}
		}
		delete(rows, oldestKey)
	}
}
