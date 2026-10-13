package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func recoveryPoller(t *testing.T, provider usage.Provider) (*Poller, *advanceClock, context.CancelFunc) {
	t.Helper()
	clock := &advanceClock{now: time.Date(2026, 10, 9, 10, 0, 0, 123, time.UTC)}
	p := New(Options{Clock: clock})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.serviceCtx = ctx
	for _, s := range p.enabledStates() {
		s.interval = time.Minute
	}
	t.Cleanup(cancel)
	return p, clock, cancel
}

func settle(p *Poller) {
	for _, s := range p.enabledStates() {
		s.fetchMu.Lock()
		s.fetchMu.Unlock()
	}
}

func Test_Recovery_retries_outage_then_resets_episode_on_success(t *testing.T) {
	// Given
	failure := providerResult{data: usage.UsageData{Error: strPtr("outage"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureTransient}}}
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{failure, failure, failure, failure, {}, failure, {}}}
	p, clock, _ := recoveryPoller(t, provider)
	p.dispatchDue(p.enabledStates())
	settle(p)
	// When / Then
	for index, delay := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute, 10 * time.Second} {
		clock.next(delay - time.Second)
		p.dispatchDue(p.enabledStates())
		settle(p)
		if got := provider.calls.Load(); got != int32(index+1) {
			t.Fatalf("early dispatch %d: calls=%d", index, got)
		}
		clock.next(time.Second)
		p.dispatchDue(p.enabledStates())
		settle(p)
		if got := provider.calls.Load(); got != int32(index+2) {
			t.Fatalf("due dispatch %d: calls=%d", index, got)
		}
	}
}

func Test_Recovery_rate_deadline_covers_manual_and_synchronous_fetches(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Codex"}
	p, clock, _ := recoveryPoller(t, provider)
	provider.responses = []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(2 * time.Minute)}}}, {}}
	p.dispatchDue(p.enabledStates())
	settle(p)
	// When
	if result, err := p.RequestRefresh(); !errors.Is(err, ErrRefreshRateLimited) || result.RetryAfterSeconds != 120 {
		t.Fatalf("protected refresh = %+v, %v", result, err)
	}
	p.PollProvider(context.Background(), "codex")
	clock.next(119 * time.Second)
	p.dispatchDue(p.enabledStates())
	settle(p)
	// Then
	if provider.calls.Load() != 1 {
		t.Fatal("rate deadline bypassed")
	}
	clock.next(time.Second)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if provider.calls.Load() != 2 {
		t.Fatal("provider did not resume at deadline")
	}
}

func Test_Recovery_refresh_coalesces_cools_down_and_cancels(t *testing.T) {
	// Given
	provider := &cancelProvider{name: "Cursor", started: make(chan struct{})}
	p, clock, cancel := recoveryPoller(t, provider)
	// When
	result, err := p.RequestRefresh()
	if err != nil || result.Status != "accepted" || result.RetryAfterSeconds != 15 {
		t.Fatalf("admission = %+v, %v", result, err)
	}
	<-provider.started
	result, err = p.RequestRefresh()
	if err != nil || result.Status != "coalesced" || result.RetryAfterSeconds <= 0 {
		t.Fatalf("coalescing = %+v, %v", result, err)
	}
	cancel()
	settle(p)
	// Then
	if _, err := p.RequestRefresh(); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("canceled service: %v", err)
	}
	p.mu.Lock()
	p.serviceCtx = context.Background()
	p.mu.Unlock()
	if result, err := p.RequestRefresh(); !errors.Is(err, ErrRefreshRateLimited) || result.RetryAfterSeconds != 15 {
		t.Fatalf("cooldown = %+v, %v", result, err)
	}
	clock.next(15 * time.Second)
	p.mu.Lock()
	p.serviceCtx = nil
	p.mu.Unlock()
}

func Test_Recovery_timeout_text_distinguishes_deadline_from_cancellation(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{{err: context.DeadlineExceeded}}}
	p, _, _ := recoveryPoller(t, provider)
	// When
	entry, _, _ := p.PollProvider(context.Background(), "Synthetic")
	// Then
	if entry.Data.Error == nil || *entry.Data.Error != "Provider fetch timed out. Will retry." {
		t.Fatalf("deadline text = %v", entry.Data.Error)
	}
}

func Test_Recovery_unknown_context_does_not_import_old_good_epoch(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{
		{data: usage.UsageData{CredentialEpoch: usage.CredentialEpoch("synthetic")}},
		{data: usage.UsageData{Error: strPtr("unknown context")}},
	}}
	p, _, _ := recoveryPoller(t, provider)
	p.PollAll(context.Background())
	// When
	entry, _, _ := p.PollProvider(context.Background(), "Synthetic")
	// Then
	if entry.Data.FetchStatus.CredentialEpoch != nil {
		t.Fatal("unknown attempt imported last-good epoch")
	}
}

func Test_Recovery_rate_fallback_doubles_and_caps(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited}}}}}
	p, clock, _ := recoveryPoller(t, provider)
	p.dispatchDue(p.enabledStates())
	settle(p)
	// When / Then
	for index, seconds := range []int{60, 120, 240, 300, 300} {
		clock.next(time.Duration(seconds-1) * time.Second)
		p.dispatchDue(p.enabledStates())
		settle(p)
		if provider.calls.Load() != int32(index+1) {
			t.Fatal("rate fallback shortened")
		}
		clock.next(time.Second)
		p.dispatchDue(p.enabledStates())
		settle(p)
		if provider.calls.Load() != int32(index+2) {
			t.Fatal("rate fallback did not resume")
		}
	}
}

func Test_Recovery_terminal_failures_keep_normal_cadence(t *testing.T) {
	for _, kind := range []usage.FailureKind{usage.FailureAuth, usage.FailureOther} {
		t.Run(string(kind), func(t *testing.T) {
			// Given
			provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{{data: usage.UsageData{Error: strPtr("terminal"), FetchFailure: &usage.FetchFailure{Kind: kind}}}}}
			p, clock, _ := recoveryPoller(t, provider)
			p.dispatchDue(p.enabledStates())
			settle(p)
			// When
			clock.next(59 * time.Second)
			p.dispatchDue(p.enabledStates())
			settle(p)
			// Then
			if provider.calls.Load() != 1 {
				t.Fatal("terminal failure accelerated")
			}
			clock.next(time.Second)
			p.dispatchDue(p.enabledStates())
			settle(p)
			if provider.calls.Load() != 2 {
				t.Fatal("terminal failure lost normal cadence")
			}
		})
	}
}

func Test_Recovery_slow_provider_does_not_delay_other_due_provider(t *testing.T) {
	// Given
	slow := &cancelProvider{name: "Cursor", started: make(chan struct{})}
	p, clock, cancel := recoveryPoller(t, slow)
	fast := &sequenceProvider{name: "Claude", responses: []providerResult{{}}}
	if err := p.Register(fast, true); err != nil {
		t.Fatal(err)
	}
	s, _ := p.enabledState("Claude")
	s.interval = time.Minute
	p.dispatchDue(p.enabledStates())
	<-slow.started
	s.fetchMu.Lock()
	s.fetchMu.Unlock()
	// When
	clock.next(time.Minute)
	p.dispatchDue(p.enabledStates())
	s.fetchMu.Lock()
	s.fetchMu.Unlock()
	// Then
	if fast.calls.Load() != 2 {
		t.Fatal("fast provider blocked behind slow provider")
	}
	cancel()
	settle(p)
}

func Test_Recovery_metadata_uses_current_attempt_after_old_good_overlay(t *testing.T) {
	// Given
	one, two := usage.CredentialEpoch("synthetic-one"), usage.CredentialEpoch("synthetic-two")
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{
		{data: usage.UsageData{CredentialEpoch: one, Current: usage.UsageBucket{Utilization: 42}}},
		{data: usage.UsageData{CredentialEpoch: two, Error: strPtr("outage"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureTransient}}},
	}}
	p, clock, _ := recoveryPoller(t, provider)
	p.PollAll(context.Background())
	clock.next(time.Second)
	// When
	entry, _, _ := p.PollProvider(context.Background(), "Claude")
	// Then
	status := entry.Data.FetchStatus
	if status == nil || status.CredentialEpoch == nil || *status.CredentialEpoch != *two || *status.CredentialEpoch == *one || status.FailureKind == nil || *status.FailureKind != usage.FailureTransient || status.FetchedAt != clock.Now().Format(time.RFC3339Nano) {
		t.Fatalf("attempt metadata = %+v", status)
	}
	*status.CredentialEpoch = "mutated"
	*status.FailureKind = usage.FailureAuth
	cache, _ := p.Get("Claude")
	if *cache.Data.FetchStatus.CredentialEpoch != *two || *cache.Data.FetchStatus.FailureKind != usage.FailureTransient {
		t.Fatal("metadata aliases cache")
	}
}
