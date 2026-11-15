package credstore

import (
	"errors"
	"log/slog"
	"sync"
)

// RefreshLog reports credential refresh problems for one store. It logs paths,
// reasons, and errors, never tokens. A nil Logger discards everything.
type RefreshLog struct {
	Logger *slog.Logger

	mu   sync.Mutex
	key  string
	pair TokenPair
}

// Refused logs a refresh refused before the exchange (a *NotWritableError in
// err) once per file and reason. Callers pass the pair they tried to refresh,
// so an external sign-in change starts a new episode, as does Saved.
func (l *RefreshLog) Refused(path string, err error, pair TokenPair) {
	var refused *NotWritableError
	if l.Logger == nil || !errors.As(err, &refused) {
		return
	}
	key := path + "\x00" + string(refused.Reason)
	l.mu.Lock()
	if l.key == key && l.pair == pair {
		l.mu.Unlock()
		return
	}
	l.key, l.pair = key, pair
	l.mu.Unlock()
	l.Logger.Warn("credential refresh refused",
		slog.String("path", path),
		slog.String("reason", string(refused.Reason)),
		slog.String("detail", refused.Reason.Phrase()),
	)
}

// Saved resets the refusal episode after a refresh was written.
func (l *RefreshLog) Saved() {
	l.mu.Lock()
	l.key, l.pair = "", TokenPair{}
	l.mu.Unlock()
}

// NotSaved logs a refresh whose new tokens never reached the file. It is not
// deduplicated: the spent sign-in may now need to be replaced.
func (l *RefreshLog) NotSaved(path string, err error) {
	if l.Logger == nil || !errors.Is(err, ErrRefreshNotSaved) {
		return
	}
	l.Logger.Warn("refreshed sign-in could not be saved",
		slog.String("path", path),
		slog.String("error", err.Error()),
	)
}

// SavedWithError logs a refresh whose tokens reached the file although a later
// step of the write, such as syncing the directory, failed.
func (l *RefreshLog) SavedWithError(path string, err error) {
	if l.Logger == nil {
		return
	}
	l.Logger.Warn("refreshed sign-in saved, but finishing the write failed",
		slog.String("path", path),
		slog.String("error", err.Error()),
	)
}
