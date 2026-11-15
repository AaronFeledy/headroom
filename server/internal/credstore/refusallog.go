package credstore

import (
	"log/slog"
	"sync"
)

// RefusalLog reports a refused refresh once per file and reason until the
// sign-in pair changes or a save succeeds. It logs paths and reasons only.
type RefusalLog struct {
	mu   sync.Mutex
	key  string
	pair TokenPair
}

// Record logs a refusal unless this file, reason, and spent pair were already
// reported. Callers pass the pair they tried to refresh, so an external
// sign-in change starts a new episode.
func (l *RefusalLog) Record(logger *slog.Logger, path string, reason NotWritableReason, pair TokenPair) {
	if logger == nil {
		return
	}
	key := path + "\x00" + string(reason)
	l.mu.Lock()
	if l.key == key && l.pair == pair {
		l.mu.Unlock()
		return
	}
	l.key, l.pair = key, pair
	l.mu.Unlock()
	logger.Warn("credential refresh refused",
		slog.String("path", path),
		slog.String("reason", string(reason)),
		slog.String("detail", reason.Phrase()),
	)
}

// Saved resets the episode after a refresh was written.
func (l *RefusalLog) Saved() {
	l.mu.Lock()
	l.key, l.pair = "", TokenPair{}
	l.mu.Unlock()
}
