package poller

import (
	"context"
	"errors"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func (s *providerState) pollLocked(ctx context.Context, now func() time.Time) Entry {
	s.mu.RLock()
	rateUntil := s.rateUntil
	entry := copyEntry(s.entry)
	s.mu.RUnlock()
	if !rateUntil.IsZero() && now().Before(rateUntil) {
		return entry
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
	s.schedule(ctx, entry, failure)
	s.entry = copyEntry(entry)
	s.hasEntry = true
	return copyEntry(entry)
}
