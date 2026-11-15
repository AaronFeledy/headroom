package credstore_test

import (
	"context"
	"errors"
	"fmt"
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

func Test_RefreshLog_reports_refusals_once_per_episode_and_resets(t *testing.T) {
	// Given
	handler := &captureHandler{}
	log := credstore.RefreshLog{Logger: slog.New(handler)}
	path := "/tmp/credentials.json"
	pair := credstore.TokenPair{Access: "old", Refresh: "old-r"}
	readOnly := &credstore.NotWritableError{Path: path, Reason: credstore.ReasonReadOnlyFile}
	cannotCreate := &credstore.NotWritableError{Path: path, Reason: credstore.ReasonCannotCreateFiles}

	// When / Then
	log.Refused(path, readOnly, pair)
	log.Refused(path, readOnly, pair)
	if got := handler.count(); got != 1 {
		t.Fatalf("records after duplicate refusal = %d, want 1", got)
	}

	// An external sign-in change starts a new episode.
	log.Refused(path, readOnly, credstore.TokenPair{Access: "external", Refresh: "external-r"})
	if got := handler.count(); got != 2 {
		t.Fatalf("records after pair change = %d, want 2", got)
	}

	// A successful save resets the episode for the same pair.
	log.Saved()
	log.Refused(path, readOnly, pair)
	if got := handler.count(); got != 3 {
		t.Fatalf("records after save reset = %d, want 3", got)
	}

	// A different reason is its own episode.
	log.Refused(path, cannotCreate, pair)
	if got := handler.count(); got != 4 {
		t.Fatalf("records after reason change = %d, want 4", got)
	}

	var discard credstore.RefreshLog
	discard.Refused(path, cannotCreate, pair)
}

func Test_RefreshLog_reports_lost_refreshes_every_time(t *testing.T) {
	// Given
	handler := &captureHandler{}
	log := credstore.RefreshLog{Logger: slog.New(handler)}
	lost := fmt.Errorf("%w: rename failed", credstore.ErrRefreshNotSaved)

	// When
	log.NotSaved("/tmp/credentials.json", lost)
	log.NotSaved("/tmp/credentials.json", lost)
	log.NotSaved("/tmp/credentials.json", errors.New("token endpoint unavailable"))

	// Then
	if got := handler.count(); got != 2 {
		t.Fatalf("records = %d, want 2 (only unsaved refreshes, not deduplicated)", got)
	}
}
