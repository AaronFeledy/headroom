package credstore_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

func (h *captureHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func Test_RefusalLog_reports_once_per_episode_and_resets(t *testing.T) {
	// Given
	handler := &captureHandler{}
	logger := slog.New(handler)
	var log credstore.RefusalLog
	path := "/tmp/credentials.json"
	pair := credstore.TokenPair{Access: "old", Refresh: "old-r"}

	// When / Then
	log.Record(logger, path, credstore.ReasonReadOnlyFile, pair)
	log.Record(logger, path, credstore.ReasonReadOnlyFile, pair)
	if got := handler.count(); got != 1 {
		t.Fatalf("records after duplicate refusal = %d, want 1", got)
	}

	// An external sign-in change starts a new episode.
	log.Record(logger, path, credstore.ReasonReadOnlyFile, credstore.TokenPair{Access: "external", Refresh: "external-r"})
	if got := handler.count(); got != 2 {
		t.Fatalf("records after pair change = %d, want 2", got)
	}

	// A successful save resets the episode for the same pair.
	log.Saved()
	log.Record(logger, path, credstore.ReasonReadOnlyFile, pair)
	if got := handler.count(); got != 3 {
		t.Fatalf("records after save reset = %d, want 3", got)
	}

	// A different reason is its own episode.
	log.Record(logger, path, credstore.ReasonCannotCreateFiles, pair)
	if got := handler.count(); got != 4 {
		t.Fatalf("records after reason change = %d, want 4", got)
	}

	log.Record(nil, path, credstore.ReasonCannotCreateFiles, pair)
}
