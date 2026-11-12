package poller

import (
	"context"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

// UpdateCredentials serializes replacement and cache invalidation with every
// fetch, so an old in-flight attempt cannot republish after the replacement.
func (p *Poller) UpdateCredentials(ctx context.Context, name string, update func() error) (Entry, bool, error) {
	state, ok := p.enabledState(name)
	if !ok {
		return Entry{}, false, nil
	}
	state.fetchMu.Lock()
	var entry Entry
	fetched := false
	defer func() {
		state.release()
		if fetched {
			p.notifyAfterPoll(state.name, entry)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Entry{}, true, err
	}
	state.mu.Lock()
	if err := update(); err != nil {
		state.mu.Unlock()
		return Entry{}, true, err
	}
	message := "Credentials updated. Usage pending."
	pending := usage.UsageData{ProviderName: state.name, ProviderType: state.providerType, Account: state.account.Clone(), Error: &message}
	state.entry = Entry{Data: pending}
	state.hasEntry = true
	state.lastGood = usage.UsageData{}
	state.hasLastGood = false
	state.mu.Unlock()
	entry, fetched = state.pollLocked(ctx, p.clock.Now)
	return entry, true, nil
}
