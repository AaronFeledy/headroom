package poller

import (
	"context"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type notifiedProvider struct {
	calls     int
	completed chan int
}

func (*notifiedProvider) Name() string { return "Claude" }
func (p *notifiedProvider) Fetch(context.Context) (usage.UsageData, error) {
	p.calls++
	p.completed <- p.calls
	return usage.UsageData{ProviderName: "Claude"}, nil
}

func Test_Recovery_Run_dispatches_due_provider_while_another_fetch_stays_blocked(t *testing.T) {
	// Given
	clock := &advanceClock{now: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)}
	ticker := newManualTicker()
	p := New(Options{Clock: clock, NewTicker: func(time.Duration) Ticker { return ticker }})
	slow := &cancelProvider{name: "Cursor", started: make(chan struct{})}
	fast := &notifiedProvider{completed: make(chan int, 2)}
	if err := p.Register(slow, true); err != nil {
		t.Fatal(err)
	}
	if err := p.Register(fast, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, time.Minute) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case <-slow.started:
	case <-deadline.C:
		t.Fatal("slow provider not started")
	}
	select {
	case <-fast.completed:
	case <-deadline.C:
		t.Fatal("fast provider not started")
	}
	state, _ := p.enabledState("Claude")
	state.fetchMu.Lock()
	state.fetchMu.Unlock()
	// When
	ticker.tick(clock.next(time.Minute))
	// Then
	select {
	case call := <-fast.completed:
		if call != 2 {
			t.Fatalf("fast call = %d", call)
		}
	case <-deadline.C:
		t.Fatal("Run blocked next due fetch behind slow provider")
	}
}
