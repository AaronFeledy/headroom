package poller

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

var ErrRefreshUnavailable = errors.New("refresh unavailable")
var ErrRefreshRateLimited = errors.New("refresh rate limited")

type RefreshResult struct {
	Status            string `json:"status"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

func (p *Poller) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return invalidIntervalError{interval: interval.String()}
	}
	states := p.enabledStates()
	p.mu.Lock()
	if p.serviceCtx != nil {
		p.mu.Unlock()
		return ErrRefreshUnavailable
	}
	p.serviceCtx = ctx
	for _, state := range states {
		state.mu.Lock()
		state.interval = interval
		state.mu.Unlock()
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.serviceCtx = nil
		p.mu.Unlock()
		p.workers.Wait()
	}()
	ticker := p.newTicker(min(interval, time.Second))
	defer ticker.Stop()
	p.dispatchDue(states)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C():
			p.dispatchDue(states)
		}
	}
}

func (p *Poller) dispatchDue(states []*providerState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.serviceCtx == nil || p.serviceCtx.Err() != nil {
		return
	}
	now := p.clock.Now()
	for _, s := range states {
		s.mu.RLock()
		due := !now.Before(s.nextDue) && !now.Before(s.rateUntil)
		s.mu.RUnlock()
		if due && s.fetchMu.TryLock() {
			p.launch(s)
		}
	}
}

func (p *Poller) RequestRefresh() (RefreshResult, error) {
	states := p.enabledStates()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.serviceCtx == nil || p.serviceCtx.Err() != nil || len(states) == 0 {
		return RefreshResult{}, ErrRefreshUnavailable
	}
	now := p.clock.Now()
	remaining := max(1, int(math.Ceil(p.refreshUntil.Sub(now).Seconds())))
	if len(p.round) > 0 {
		return RefreshResult{Status: "coalesced", RetryAfterSeconds: remaining}, nil
	}
	var earliestProvider time.Time
	for i, s := range states {
		s.mu.RLock()
		rateUntil := s.rateUntil
		s.mu.RUnlock()
		if i == 0 || rateUntil.Before(earliestProvider) {
			earliestProvider = rateUntil
		}
	}
	admissionAt := p.refreshUntil
	if earliestProvider.After(admissionAt) {
		admissionAt = earliestProvider
	}
	if now.Before(admissionAt) {
		return RefreshResult{RetryAfterSeconds: max(1, int(math.Ceil(admissionAt.Sub(now).Seconds())))}, ErrRefreshRateLimited
	}
	p.refreshUntil = now.Add(15 * time.Second)
	p.round = make(map[*providerState]bool)
	for _, s := range states {
		s.mu.RLock()
		limited := now.Before(s.rateUntil)
		spaced := s.insideSpacingWindow(now)
		s.mu.RUnlock()
		// A spaced provider is skipped for this round without delaying others.
		if limited || spaced {
			continue
		}
		p.round[s] = true
		if s.fetchMu.TryLock() {
			p.launch(s)
		}
	}
	return RefreshResult{Status: "accepted", RetryAfterSeconds: 15}, nil
}

// The caller reserves fetchMu before spawning: no async worker queues behind it.
func (p *Poller) launch(s *providerState) {
	ctx, cancel := context.WithTimeout(p.serviceCtx, 30*time.Second)
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		defer cancel()
		entry, fetched := s.pollAndRelease(ctx, p.clock.Now)
		if fetched {
			p.notifyAfterPoll(s.name, entry)
		}
	}()
}

func (s *providerState) release() {
	s.owner.mu.Lock()
	s.fetchMu.Unlock()
	delete(s.owner.round, s)
	s.owner.mu.Unlock()
}

func (s *providerState) schedule(ctx context.Context, entry Entry, failure *usage.FetchFailure, responded bool) {
	now := entry.FetchedAt
	success := entry.Data.Error == nil
	delay := s.scheduledDelay()
	if success {
		s.transientRetries = 0
	}
	if failure != nil {
		switch failure.Kind {
		case usage.FailureTransient:
			if !success && !errors.Is(ctx.Err(), context.Canceled) && s.transientRetries < 3 {
				retry := 10 * time.Second << s.transientRetries
				if responded && s.hasPolicy() {
					// A real response restarts adaptive spacing, so an earlier
					// transient retry would not be the real next attempt.
					delay = max(delay, retry)
				} else {
					delay = min(delay, retry)
				}
				s.transientRetries++
			}
		case usage.FailureRateLimited:
		case usage.FailureAuth, usage.FailureOther:
		}
	}
	if failure != nil && (failure.Kind == usage.FailureRateLimited || !failure.RetryAfter.IsZero() || failure.RateLimitFallback) {
		cooldown := max(0, failure.RetryAfter.Sub(now))
		// Zero or elapsed deadlines must not turn a 429 into a rapid retry loop.
		if cooldown == 0 || failure.RateLimitFallback {
			if s.hasPolicy() && usageRateLimited(failure) {
				// The poller owns adaptive spacing; the provider only reports
				// the bare upstream header now.
				cooldown = s.spacing
			} else {
				// Preserve the pre-adaptive backoff: its floor was the provider's
				// minimum spacing, never the idle delay.
				base := max(s.interval, s.spacing)
				s.rateDelay = min(max(base, 5*time.Minute), max(max(base, time.Minute), s.rateDelay*2))
				cooldown = max(cooldown, s.rateDelay)
			}
		}
		if until := now.Add(cooldown); until.After(s.rateUntil) {
			s.rateUntil = until
		}
		if s.hasPolicy() || success {
			delay = max(delay, cooldown)
		} else {
			switch failure.Kind {
			case usage.FailureRateLimited:
				delay = cooldown
			case usage.FailureTransient, usage.FailureAuth, usage.FailureOther:
				delay = max(delay, cooldown)
			}
		}
	} else if failure == nil || success {
		s.rateDelay = 0
	}
	s.nextDue = now.Add(delay)
}
