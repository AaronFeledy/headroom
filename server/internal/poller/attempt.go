package poller

import (
	"context"
	"errors"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// pollAndRelease releases the fetch lock before an observer can start another
// poll for the same provider. The deferred release still covers a panic.
// fetched is false when the provider was not contacted, so callers must not
// treat the returned cached reading as a fresh observation.
func (s *providerState) pollAndRelease(ctx context.Context, now func() time.Time) (entry Entry, fetched bool) {
	released := false
	defer func() {
		if !released {
			s.release()
		}
	}()
	entry, fetched = s.pollLocked(ctx, now)
	released = true
	s.release()
	return entry, fetched
}

func (s *providerState) pollLocked(ctx context.Context, now func() time.Time) (Entry, bool) {
	current := now()
	s.mu.RLock()
	rateUntil := s.rateUntil
	spaced := s.insideSpacingWindow(current)
	entry := copyEntry(s.entry)
	s.mu.RUnlock()
	if !rateUntil.IsZero() && current.Before(rateUntil) {
		return entry, false
	}
	if spaced {
		// Spacing is not a rate-limit cooldown: only the next due time moves.
		s.mu.Lock()
		if until := s.responseAt.Add(s.spacing); s.nextDue.Before(until) {
			s.nextDue = until
		}
		s.mu.Unlock()
		return entry, false
	}

	data, err := fetchProvider(ctx, s.name, s.provider)

	s.mu.Lock()
	defer s.mu.Unlock()
	completed := now().UTC()
	failure := data.FetchFailure
	if failure == nil {
		var typed *usage.FetchFailure
		if errors.As(err, &typed) {
			failure = typed
		}
	}
	var kind *usage.FailureKind
	if err != nil || data.Error != nil {
		value := usage.FailureOther
		if failure != nil {
			value = failure.Kind
		} else if err == nil && data.NeedsReauth {
			value = usage.FailureAuth
		}
		kind = &value
	}
	entry = Entry{Data: s.normalize(data, err), FetchedAt: completed}
	entry.Data.CredentialEpoch = copyString(data.CredentialEpoch)
	entry.Data.FetchStatus = &usage.FetchStatus{FetchedAt: completed.Format(time.RFC3339Nano), FailureKind: kind, CredentialEpoch: copyString(data.CredentialEpoch)}
	if kind != nil && failure == nil {
		failure = &usage.FetchFailure{Kind: *kind}
	}
	entry.Data.FetchFailure = copyFetchFailure(failure)
	// Distinguish an upstream reply from a transport failure that never
	// reached the provider.
	responded := err == nil && entry.Data.Error == nil
	if failure != nil && failure.HTTPStatus != 0 {
		responded = true
	}
	if responded {
		s.responseAt = completed
	}
	success := entry.Data.Error == nil
	s.learnSpacing(success, failure)
	if success {
		s.recordReading(entry.Data)
	} else {
		s.idle = 0
	}
	s.schedule(ctx, entry, failure, responded)
	entry.Data.FetchStatus.Diagnostics = s.diagnostics(entry, failure)
	s.logAttempt(ctx, entry, failure)
	s.entry = copyEntry(entry)
	s.hasEntry = true
	return copyEntry(entry), true
}
