package poller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

var claudePolicy = usage.PollPolicy{MinSpacing: 3 * time.Minute, MaxSpacing: 15 * time.Minute, IdleSpacing: 10 * time.Minute}

type policyProvider struct {
	name   string
	policy usage.PollPolicy
	calls  atomic.Int32
	fail   bool
}

func (p *policyProvider) Name() string { return p.name }

func (p *policyProvider) PollPolicy() usage.PollPolicy { return p.policy }

func (p *policyProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	if err := ctx.Err(); err != nil {
		return usage.UsageData{ProviderName: p.name}, err
	}
	call := p.calls.Add(1)
	if p.fail {
		return usage.UsageData{ProviderName: p.name}, usage.TransportFailure(errors.New("dial failed"))
	}
	return usage.UsageData{ProviderName: p.name, Current: usage.UsageBucket{Utilization: float64(call)}}, nil
}

func spacingPoller(t *testing.T, providers ...usage.Provider) (*Poller, *advanceClock) {
	t.Helper()
	clock := &advanceClock{now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	p := New(Options{Clock: clock})
	for _, provider := range providers {
		if err := p.Register(provider, true); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.serviceCtx = ctx
	for _, state := range p.enabledStates() {
		state.interval = time.Minute
	}
	t.Cleanup(cancel)
	return p, clock
}

func Test_Poller_minimum_spacing_covers_tick_refresh_poll_and_credentials(t *testing.T) {
	// Given a provider with a three-minute floor and a normal one-minute peer.
	claude := &policyProvider{name: "Claude", policy: claudePolicy}
	other := &policyProvider{name: "Other"}
	p, clock := spacingPoller(t, claude, other)
	ctx := context.Background()
	base := clock.Now()
	// The first fetch is never delayed.
	p.PollAll(ctx)
	if claude.calls.Load() != 1 || other.calls.Load() != 1 {
		t.Fatalf("first poll calls = claude %d other %d", claude.calls.Load(), other.calls.Load())
	}
	d, ok := p.Get("Claude")
	if !ok {
		t.Fatal("missing cached entry")
	}
	next, err := time.Parse(time.RFC3339Nano, d.Data.FetchStatus.Diagnostics.NextAttemptAt)
	if err != nil || !next.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("next_attempt_at = %v, want %s", d.Data.FetchStatus.Diagnostics.NextAttemptAt, base.Add(3*time.Minute))
	}
	if d.Data.FetchStatus.Diagnostics.CooldownUntil != nil || d.Data.FetchStatus.Diagnostics.CooldownSource != nil {
		t.Fatal("spacing was reported as a rate-limit cooldown")
	}
	// When one minute passes, synchronous reads and credential updates are
	// served from cache while the peer keeps its configured cadence.
	clock.next(time.Minute)
	if _, _, err := p.PollProvider(ctx, "Claude"); err != nil || claude.calls.Load() != 1 {
		t.Fatalf("spaced PollProvider fetched: calls=%d err=%v", claude.calls.Load(), err)
	}
	if _, _, err := p.PollProvider(ctx, "Other"); err != nil || other.calls.Load() != 2 {
		t.Fatalf("peer PollProvider calls=%d", other.calls.Load())
	}
	if _, _, err := p.UpdateCredentials(ctx, "Claude", func() error { return nil }); err != nil || claude.calls.Load() != 1 {
		t.Fatalf("spaced UpdateCredentials fetched: calls=%d err=%v", claude.calls.Load(), err)
	}
	result, err := p.RequestRefresh()
	if err != nil || result.Status != "accepted" {
		t.Fatalf("refresh = %+v, %v", result, err)
	}
	settle(p)
	if claude.calls.Load() != 1 || other.calls.Load() != 3 {
		t.Fatalf("refresh calls = claude %d other %d", claude.calls.Load(), other.calls.Load())
	}
	// Then scheduled dispatch follows each provider's own due time.
	clock.next(time.Minute)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if claude.calls.Load() != 1 || other.calls.Load() != 4 {
		t.Fatalf("two-minute dispatch calls = claude %d other %d", claude.calls.Load(), other.calls.Load())
	}
	clock.next(time.Minute)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if claude.calls.Load() != 2 || other.calls.Load() != 5 {
		t.Fatalf("three-minute dispatch calls = claude %d other %d", claude.calls.Load(), other.calls.Load())
	}
}

func Test_Poller_transport_failure_keeps_transient_retry_inside_spacing_window(t *testing.T) {
	// Given
	claude := &policyProvider{name: "Claude", policy: claudePolicy, fail: true}
	p, clock := spacingPoller(t, claude)
	ctx := context.Background()
	p.PollProvider(ctx, "Claude")
	if claude.calls.Load() != 1 {
		t.Fatal("first fetch missing")
	}
	// When a transport failure had no HTTP response, the transient retry runs.
	clock.next(10 * time.Second)
	claude.fail = false
	p.PollProvider(ctx, "Claude")
	if claude.calls.Load() != 2 {
		t.Fatalf("transient retry calls = %d, want 2", claude.calls.Load())
	}
	// Then the successful reply starts the spacing window again.
	clock.next(10 * time.Second)
	p.PollProvider(ctx, "Claude")
	if claude.calls.Load() != 2 {
		t.Fatalf("spacing did not follow a real response: calls=%d", claude.calls.Load())
	}
}
