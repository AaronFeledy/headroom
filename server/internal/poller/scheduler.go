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
		s.mu.RUnlock()
		if limited {
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
		defer s.release()
		s.pollLocked(ctx, p.clock.Now)
	}()
}

func (s *providerState) release() {
	s.owner.mu.Lock()
	s.fetchMu.Unlock()
	delete(s.owner.round, s)
	s.owner.mu.Unlock()
}

func (s *providerState) schedule(ctx context.Context, entry Entry, failure *usage.FetchFailure) {
	now := entry.FetchedAt
	success := entry.Data.Error == nil
	delay := s.interval
	if success {
		s.transientRetries = 0
	}
	if failure != nil {
		switch failure.Kind {
		case usage.FailureTransient:
			if !success && !errors.Is(ctx.Err(), context.Canceled) && s.transientRetries < 3 {
				delay = min(delay, 10*time.Second<<s.transientRetries)
				s.transientRetries++
			}
		case usage.FailureRateLimited:
		case usage.FailureAuth, usage.FailureOther:
		}
	}
	if failure != nil && (failure.Kind == usage.FailureRateLimited || !failure.RetryAfter.IsZero() || failure.RateLimitFallback) {
		cooldown := max(0, failure.RetryAfter.Sub(now))
		if failure.RetryAfter.IsZero() || failure.RateLimitFallback {
			s.rateDelay = min(max(s.interval, 5*time.Minute), max(max(s.interval, time.Minute), s.rateDelay*2))
			cooldown = max(cooldown, s.rateDelay)
		}
		s.rateUntil = now.Add(cooldown)
		if success {
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
