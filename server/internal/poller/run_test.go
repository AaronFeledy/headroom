package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Poller_Run_rejects_nonpositive_interval_polls_immediately_and_stops_on_context(t *testing.T) {
	// Given
	manual := newManualTicker()
	clock := &advanceClock{now: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}
	poller := New(Options{Clock: clock, NewTicker: func(time.Duration) Ticker { return manual }})
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Claude", Current: usage.UsageBucket{Utilization: 1}}},
		{data: usage.UsageData{ProviderName: "Claude", Current: usage.UsageBucket{Utilization: 2}}},
	}}
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if err := poller.Run(context.Background(), 0); !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("Run zero interval err = %v, want ErrInvalidInterval", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// When
	go func() { done <- poller.Run(ctx, time.Minute) }()
	for provider.calls.Load() == 0 {
		runtimeGosched()
	}
	state, _ := poller.enabledState("Claude")
	state.fetchMu.Lock()
	state.fetchMu.Unlock()
	manual.tick(clock.next(time.Minute))
	for provider.calls.Load() < 2 {
		runtimeGosched()
	}
	cancel()
	err := <-done

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context canceled", err)
	}
	if !manual.stopped.Load() {
		t.Fatal("ticker was not stopped")
	}
}

func Test_Poller_PollProvider_propagates_cancellation_to_hung_provider(t *testing.T) {
	// Given
	poller := New(Options{Clock: fixedClock{now: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}})
	provider := &cancelProvider{name: "Cursor", started: make(chan struct{})}
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Entry, 1)

	// When
	go func() { entry, _, _ := poller.PollProvider(ctx, "cursor"); done <- entry }()
	<-provider.started
	cancel()
	entry := <-done

	// Then
	if entry.Data.Error == nil || *entry.Data.Error != "Provider fetch canceled." {
		t.Fatalf("entry = %+v", entry)
	}
}

func Test_Poller_Register_rejects_malformed_provider_registration(t *testing.T) {
	// Given
	poller := New(Options{})

	// When / Then
	if err := poller.Register(nil, true); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("nil provider err = %v, want ErrInvalidProvider", err)
	}
	if err := poller.Register(&sequenceProvider{name: "   "}, true); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("blank provider err = %v, want ErrInvalidProvider", err)
	}
	if err := poller.Register(&sequenceProvider{name: "Claude"}, true); err != nil {
		t.Fatalf("first register err = %v", err)
	}
	if err := poller.Register(&sequenceProvider{name: "claude"}, true); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("duplicate provider err = %v, want ErrInvalidProvider", err)
	}
}
