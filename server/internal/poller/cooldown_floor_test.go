package poller

import (
	"context"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Recovery_MinCooldown_floor_preserves_header_and_diagnostics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter time.Duration
		floor      time.Duration
		want       time.Duration
		source     string
	}{
		{"elapsed header uses floor", 0, 5 * time.Minute, 5 * time.Minute, "backoff"},
		{"future header is honored", 10 * time.Minute, 5 * time.Minute, 10 * time.Minute, "retry_after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			base := time.Date(2026, 10, 9, 10, 0, 0, 123, time.UTC)
			failure := &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: base.Add(tc.retryAfter), MinCooldown: tc.floor}
			provider := &sequenceProvider{name: "Claude", responses: []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: failure}}, {}}}
			p, clock, _ := recoveryPoller(t, provider)
			p.dispatchDue(p.enabledStates())
			settle(p)
			entry := p.Snapshot()[0]
			state := p.enabledStates()[0]
			want := entry.FetchedAt.Add(tc.want)
			// Then the floor and diagnostics keep distinct meanings.
			state.mu.RLock()
			rateUntil := state.rateUntil
			state.mu.RUnlock()
			if !rateUntil.Equal(want) {
				t.Fatalf("rateUntil = %s, want %s", rateUntil, want)
			}
			d := entry.Data.FetchStatus.Diagnostics
			cooldown, err := time.Parse(time.RFC3339Nano, *d.CooldownUntil)
			if err != nil || !cooldown.Equal(want) {
				t.Fatalf("cooldown_until = %v, want %s", d.CooldownUntil, want)
			}
			retryAfter, err := time.Parse(time.RFC3339Nano, *d.RetryAfter)
			if err != nil || !retryAfter.Equal(failure.RetryAfter.UTC()) {
				t.Fatalf("retry_after = %v, want the upstream header %s", d.RetryAfter, failure.RetryAfter.UTC())
			}
			if d.CooldownSource == nil || *d.CooldownSource != tc.source {
				t.Fatalf("cooldown_source = %v, want %s", d.CooldownSource, tc.source)
			}
			// And no earlier dispatch is allowed.
			clock.next(tc.want - time.Second)
			p.dispatchDue(p.enabledStates())
			settle(p)
			if got := provider.calls.Load(); got != 1 {
				t.Fatalf("early dispatch calls = %d", got)
			}
			clock.next(time.Second)
			p.dispatchDue(p.enabledStates())
			settle(p)
			if got := provider.calls.Load(); got != 2 {
				t.Fatalf("due dispatch calls = %d", got)
			}
		})
	}
}

func Test_Recovery_MinCooldown_never_shortens_existing_rate_deadline(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude"}
	p, clock, _ := recoveryPoller(t, provider)
	state := p.enabledStates()[0]
	now := clock.Now()
	later := now.Add(10 * time.Minute)
	state.mu.Lock()
	state.rateUntil = later
	state.mu.Unlock()
	// When a shorter provider floor is scheduled.
	state.schedule(context.Background(), Entry{FetchedAt: now, Data: usage.UsageData{Error: strPtr("limited")}}, &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: now, MinCooldown: 5 * time.Minute})
	// Then
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.rateUntil.Equal(later) {
		t.Fatalf("rateUntil = %s, want the later existing %s", state.rateUntil, later)
	}
}
