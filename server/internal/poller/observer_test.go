package poller

import (
	"context"
	"testing"
	"time"
)

type observation struct {
	name  string
	entry Entry
}

// observeAfterPoll records every notification. The buffer is large enough that
// a correct run never blocks its own observer goroutine.
func observeAfterPoll(p *Poller) chan observation {
	observed := make(chan observation, 16)
	p.SetAfterPoll(func(name string, entry Entry) {
		observed <- observation{name: name, entry: entry}
	})
	return observed
}

func waitObservation(t *testing.T, observed <-chan observation) observation {
	t.Helper()
	select {
	case got := <-observed:
		return got
	case <-time.After(time.Second):
		t.Fatal("post-poll observer was not notified")
		return observation{}
	}
}

// requireNoObservation is a negative assertion. The cached paths decide
// synchronously not to notify, so the immediate check already covers them; the
// bounded wait also catches an extra notification goroutine spawned by mistake.
func requireNoObservation(t *testing.T, observed <-chan observation) {
	t.Helper()
	select {
	case got := <-observed:
		t.Fatalf("unexpected post-poll notification: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func Test_PostPoll_scheduled_tick_notifies_once(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{percentReading(10)}}
	p, _, _ := recoveryPoller(t, provider)
	observed := observeAfterPoll(p)

	// When
	p.dispatchDue(p.enabledStates())
	p.workers.Wait()

	// Then
	got := waitObservation(t, observed)
	if got.name != "Claude" || got.entry.FetchedAt.IsZero() {
		t.Fatalf("observation = %+v", got)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	requireNoObservation(t, observed)
}

func Test_PostPoll_request_refresh_notifies(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{percentReading(10)}}
	p, _, _ := recoveryPoller(t, provider)
	observed := observeAfterPoll(p)

	// When
	if _, err := p.RequestRefresh(); err != nil {
		t.Fatal(err)
	}
	p.workers.Wait()

	// Then
	if got := waitObservation(t, observed); got.name != "Claude" {
		t.Fatalf("observation = %+v", got)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
}

func Test_PostPoll_all_polls_notify_once_per_fetch(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{percentReading(10)}}
	p := New(Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	observed := observeAfterPoll(p)

	// When
	entries := p.PollAll(context.Background())

	// Then
	if len(entries) != 1 || provider.calls.Load() != 1 {
		t.Fatalf("entries = %d, calls = %d", len(entries), provider.calls.Load())
	}
	if got := waitObservation(t, observed); got.name != "Claude" || got.entry.FetchedAt.IsZero() {
		t.Fatalf("observation = %+v", got)
	}
	requireNoObservation(t, observed)
}

func Test_PostPoll_update_credentials_notifies_after_its_fetch(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Cursor", responses: []providerResult{percentReading(10)}}
	p := New(Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	observed := observeAfterPoll(p)

	// When
	entry, ok, err := p.UpdateCredentials(context.Background(), "Cursor", func() error { return nil })

	// Then
	if err != nil || !ok || entry.FetchedAt.IsZero() {
		t.Fatalf("update = %+v, ok=%v, error=%v", entry, ok, err)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	if got := waitObservation(t, observed); got.name != "Cursor" {
		t.Fatalf("observation = %+v", got)
	}
}

func Test_PostPoll_rate_limit_cooldown_does_not_notify(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{percentReading(10)}}
	p, clock, _ := recoveryPoller(t, provider)
	observed := observeAfterPoll(p)
	first := pollProvider(t, p, "Claude")
	waitObservation(t, observed)
	state := policyState(t, p)
	state.mu.Lock()
	state.rateUntil = clock.Now().Add(time.Minute)
	state.mu.Unlock()

	// When
	entry := pollProvider(t, p, "Claude")

	// Then
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("cooldown fetched again: calls = %d", calls)
	}
	if !entry.FetchedAt.Equal(first.FetchedAt) {
		t.Fatal("cooldown did not return the cached entry")
	}
	requireNoObservation(t, observed)

	state.fetchMu.Lock()
	_, fetched := state.pollAndRelease(context.Background(), p.clock.Now)
	if fetched {
		t.Fatal("cached cooldown return reported a fetch")
	}
}

func Test_PostPoll_spacing_window_does_not_notify(t *testing.T) {
	// Given
	provider, p, _ := adaptivePoller(t, claudePolicy)
	observed := observeAfterPoll(p)
	first := pollProvider(t, p, "Claude")
	waitObservation(t, observed)

	// When the same clock instant is still inside the learned spacing window.
	entry := pollProvider(t, p, "Claude")

	// Then
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("spacing window fetched again: calls = %d", calls)
	}
	if !entry.FetchedAt.Equal(first.FetchedAt) {
		t.Fatal("spacing window did not return the cached entry")
	}
	requireNoObservation(t, observed)
}

func Test_PostPoll_real_fetch_notifies_exactly_once(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Codex", responses: []providerResult{percentReading(10)}}
	p := New(Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	observed := observeAfterPoll(p)

	// When
	p.PollProvider(context.Background(), "Codex")

	// Then
	if got := waitObservation(t, observed); got.name != "Codex" || got.entry.FetchedAt.IsZero() {
		t.Fatalf("observation = %+v", got)
	}
	requireNoObservation(t, observed)
}
