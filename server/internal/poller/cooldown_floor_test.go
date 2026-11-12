package poller

import (
	"context"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Recovery_learned_spacing_sets_cooldown_and_preserves_header(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter func(time.Time) time.Time
		fallback   bool
		want       time.Duration
		source     string
		header     bool
	}{
		{"elapsed header uses spacing", func(base time.Time) time.Time { return base.Add(-time.Second) }, false, 6 * time.Minute, "backoff", true},
		{"missing header uses spacing", func(time.Time) time.Time { return time.Time{} }, true, 6 * time.Minute, "backoff", false},
		{"future header is honored", func(base time.Time) time.Time { return base.Add(10 * time.Minute) }, false, 10 * time.Minute, "retry_after", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, p, clock := adaptivePoller(t, claudePolicy)
			failure := rateLimit(usage.OperationUsage, tc.retryAfter(clock.Now()), tc.fallback)
			provider.responses = []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: failure}}}
			p.dispatchDue(p.enabledStates())
			settle(p)
			entry := p.Snapshot()[0]
			state := p.enabledStates()[0]
			want := entry.FetchedAt.Add(tc.want)
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
			if (d.RetryAfter != nil) != tc.header {
				t.Fatalf("retry_after present = %t, want %t", d.RetryAfter != nil, tc.header)
			}
			if tc.header && *d.RetryAfter != failure.RetryAfter.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("retry_after = %v, want the upstream header %s", d.RetryAfter, failure.RetryAfter.UTC())
			}
			if d.CooldownSource == nil || *d.CooldownSource != tc.source {
				t.Fatalf("cooldown_source = %v, want %s", d.CooldownSource, tc.source)
			}
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

func Test_Recovery_learned_spacing_never_shortens_existing_rate_deadline(t *testing.T) {
	_, p, clock := adaptivePoller(t, claudePolicy)
	state := p.enabledStates()[0]
	now := clock.Now()
	later := now.Add(10 * time.Minute)
	state.mu.Lock()
	state.rateUntil = later
	state.spacing = 6 * time.Minute
	state.mu.Unlock()
	state.schedule(context.Background(), Entry{FetchedAt: now, Data: usage.UsageData{Error: strPtr("limited")}}, rateLimit(usage.OperationUsage, now, true), true)
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.rateUntil.Equal(later) {
		t.Fatalf("rateUntil = %s, want the later existing %s", state.rateUntil, later)
	}
}
