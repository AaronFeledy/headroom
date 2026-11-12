package poller

import (
	"context"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_AfterPoll_observes_after_the_fetch_lock_is_released(t *testing.T) {
	// Given
	p := New(Options{})
	provider := &sequenceProvider{name: "Claude", responses: []providerResult{{data: usage.UsageData{ProviderName: "Claude"}}}}
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	observed := make(chan Entry, 1)
	available := make(chan bool, 1)
	p.SetAfterPoll(func(name string, entry Entry) {
		state, ok := p.enabledState(name)
		if !ok {
			available <- false
			return
		}
		acquired := state.fetchMu.TryLock()
		if acquired {
			state.fetchMu.Unlock()
		}
		available <- acquired
		observed <- entry
	})

	// When
	entry, ok, err := p.PollProvider(context.Background(), "Claude")

	// Then
	if err != nil || !ok || entry.Data.ProviderName != "Claude" {
		t.Fatalf("poll = %+v, ok=%v, error=%v", entry, ok, err)
	}
	select {
	case acquired := <-available:
		if !acquired {
			t.Fatal("after-poll observer ran while the fetch lock was held")
		}
	case <-time.After(time.Second):
		t.Fatal("after-poll observer did not run")
	}
	select {
	case got := <-observed:
		if got.Data.ProviderName != "Claude" {
			t.Fatalf("observed entry = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("after-poll observer did not receive an entry")
	}
}
