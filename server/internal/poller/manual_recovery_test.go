package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Recovery_refresh_unavailable_when_no_providers_enabled(t *testing.T) {
	// Given
	p := New(Options{})
	p.serviceCtx = context.Background()
	// When
	_, err := p.RequestRefresh()
	// Then
	if !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("empty service: %v", err)
	}
}

func Test_Recovery_valid_rate_deadline_overrides_fallback_delay(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Synthetic"}
	p, clock, _ := recoveryPoller(t, provider)
	provider.responses = []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(10 * time.Second)}}}, {}}
	p.dispatchDue(p.enabledStates())
	settle(p)
	// When
	clock.next(10 * time.Second)
	p.dispatchDue(p.enabledStates())
	settle(p)
	// Then
	if provider.calls.Load() != 2 {
		t.Fatal("valid Retry-After replaced by fallback backoff")
	}
}

func Test_Recovery_manual_refresh_does_not_reset_transient_episode(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Synthetic", responses: []providerResult{{data: usage.UsageData{Error: strPtr("outage"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureTransient}}}}}
	p, clock, _ := recoveryPoller(t, provider)
	p.dispatchDue(p.enabledStates())
	settle(p)
	// When
	if _, err := p.RequestRefresh(); err != nil {
		t.Fatal(err)
	}
	settle(p)
	// Then
	clock.next(19 * time.Second)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if provider.calls.Load() != 2 {
		t.Fatal("manual refresh reset transient budget")
	}
	clock.next(time.Second)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if provider.calls.Load() != 3 {
		t.Fatal("next transient retry not due")
	}
}

func Test_Refresh_admission_rejects_all_rate_protected_providers(t *testing.T) {
	// Given
	first := &sequenceProvider{name: "First"}
	p, clock, _ := recoveryPoller(t, first)
	first.responses = []providerResult{{data: usage.UsageData{Error: strPtr("HTTP 429"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(2 * time.Minute)}}}, {}}
	second := &sequenceProvider{name: "Second", responses: []providerResult{{data: usage.UsageData{Error: strPtr("HTTP 429"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(3 * time.Minute)}}}}}
	if err := p.Register(second, true); err != nil {
		t.Fatal(err)
	}
	p.dispatchDue(p.enabledStates())
	settle(p)
	clock.next(500 * time.Millisecond)
	// When
	result, err := p.RequestRefresh()
	settle(p)
	// Then
	if !errors.Is(err, ErrRefreshRateLimited) || result.RetryAfterSeconds != 120 || result.Status != "" {
		t.Fatalf("protected admission = %+v, %v", result, err)
	}
	p.mu.Lock()
	admissionStarted := len(p.round) != 0 || !p.refreshUntil.IsZero()
	p.mu.Unlock()
	if first.calls.Load() != 1 || second.calls.Load() != 1 || admissionStarted {
		t.Fatal("rejected refresh started work or admission cooldown")
	}
}

func Test_Refresh_admission_starts_only_eligible_provider_when_others_rate_protected(t *testing.T) {
	// Given
	protected := &sequenceProvider{name: "Protected"}
	p, clock, cancel := recoveryPoller(t, protected)
	protected.responses = []providerResult{{data: usage.UsageData{Error: strPtr("HTTP 429"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(2 * time.Minute)}}}}
	p.dispatchDue(p.enabledStates())
	settle(p)
	eligible := &cancelProvider{name: "Eligible", started: make(chan struct{})}
	if err := p.Register(eligible, true); err != nil {
		t.Fatal(err)
	}
	// When
	result, err := p.RequestRefresh()
	if err != nil || result.Status != "accepted" {
		t.Fatalf("mixed admission = %+v, %v", result, err)
	}
	<-eligible.started
	// Then
	if result.RetryAfterSeconds != 15 || protected.calls.Load() != 1 {
		t.Fatalf("mixed admission = %+v; protected calls=%d", result, protected.calls.Load())
	}
	cancel()
	settle(p)
}
