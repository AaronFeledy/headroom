package poller

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type Entry struct {
	Data      usage.UsageData
	FetchedAt time.Time
}

type Options struct {
	Clock     Clock
	NewTicker func(time.Duration) Ticker
}

type Poller struct {
	clock        Clock
	newTicker    func(time.Duration) Ticker
	mu           sync.RWMutex
	providers    map[string]*providerState
	serviceCtx   context.Context
	refreshUntil time.Time
	round        map[*providerState]bool
	workers      sync.WaitGroup
}

type providerState struct {
	name             string
	provider         usage.Provider
	enabled          bool
	fetchMu          sync.Mutex
	mu               sync.RWMutex
	entry            Entry
	hasEntry         bool
	lastGood         usage.UsageData
	hasLastGood      bool
	owner            *Poller
	interval         time.Duration
	nextDue          time.Time
	rateUntil        time.Time
	transientRetries int
	rateDelay        time.Duration
}

func New(opts Options) *Poller {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	newTicker := opts.NewTicker
	if newTicker == nil {
		newTicker = newRealTicker
	}
	return &Poller{clock: clock, newTicker: newTicker, providers: map[string]*providerState{}}
}

func (p *Poller) Register(provider usage.Provider, enabled bool) error {
	if provider == nil {
		return invalidProviderError{reason: "nil provider"}
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return invalidProviderError{reason: "blank provider name"}
	}
	key := providerKey(name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.providers[key]; exists {
		return invalidProviderError{reason: "duplicate provider " + name}
	}
	p.providers[key] = &providerState{name: name, provider: provider, enabled: enabled, owner: p}
	return nil
}

func (p *Poller) PollAll(ctx context.Context) []Entry {
	states := p.enabledStates()
	entries := make([]Entry, len(states))
	var wg sync.WaitGroup
	var busy []int
	for i, state := range states {
		if !state.fetchMu.TryLock() {
			busy = append(busy, i)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer state.release()
			entries[i] = state.pollLocked(ctx, p.clock.Now)
		}()
	}
	for _, i := range busy {
		entries[i] = states[i].poll(ctx, p.clock.Now)
	}
	wg.Wait()
	sortEntries(entries)
	return entries
}

func (p *Poller) PollProvider(ctx context.Context, name string) (Entry, bool, error) {
	state, ok := p.enabledState(name)
	if !ok {
		return Entry{}, false, nil
	}
	return state.poll(ctx, p.clock.Now), true, nil
}

func (p *Poller) Snapshot() []Entry {
	states := p.enabledStates()
	entries := make([]Entry, 0, len(states))
	for _, state := range states {
		state.mu.RLock()
		if state.hasEntry {
			entries = append(entries, copyEntry(state.entry))
		}
		state.mu.RUnlock()
	}
	sortEntries(entries)
	return entries
}

func (p *Poller) Get(name string) (Entry, bool) {
	state, ok := p.enabledState(name)
	if !ok {
		return Entry{}, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.hasEntry {
		return Entry{}, false
	}
	return copyEntry(state.entry), true
}

func (p *Poller) enabledState(name string) (*providerState, bool) {
	p.mu.RLock()
	state, ok := p.providers[providerKey(name)]
	p.mu.RUnlock()
	if !ok || !state.enabled {
		return nil, false
	}
	return state, true
}

func (p *Poller) enabledStates() []*providerState {
	p.mu.RLock()
	states := make([]*providerState, 0, len(p.providers))
	for _, state := range p.providers {
		if state.enabled {
			states = append(states, state)
		}
	}
	p.mu.RUnlock()
	sort.Slice(states, func(i int, j int) bool {
		return strings.ToLower(states[i].name) < strings.ToLower(states[j].name)
	})
	return states
}

func (s *providerState) poll(ctx context.Context, now func() time.Time) Entry {
	s.fetchMu.Lock()
	defer s.release()
	return s.pollLocked(ctx, now)
}

func fetchProvider(ctx context.Context, name string, provider usage.Provider) (data usage.UsageData, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			data = usage.UsageData{ProviderName: name}
			err = providerPanicError{}
		}
	}()
	return provider.Fetch(ctx)
}

func (s *providerState) normalize(data usage.UsageData, err error) usage.UsageData {
	if data.ProviderName == "" {
		data.ProviderName = s.name
	}
	if err == nil && data.Error == nil {
		s.lastGood = copyUsageData(data)
		s.hasLastGood = true
		return copyUsageData(data)
	}
	if err == nil {
		return s.overlayExpectedError(data)
	}
	return s.overlayFetchFailure(data, err)
}

func (s *providerState) overlayExpectedError(data usage.UsageData) usage.UsageData {
	if !s.hasLastGood {
		return copyUsageData(data)
	}
	overlay := copyUsageData(s.lastGood)
	overlay.Auth = copyAuth(data.Auth)
	overlay.Error = copyString(data.Error)
	overlay.NeedsReauth = data.NeedsReauth
	overlay.ReauthCommand = copyString(data.ReauthCommand)
	overlay.PrimaryStatusText = copyString(data.PrimaryStatusText)
	overlay.SecondaryStatusText = copyString(data.SecondaryStatusText)
	return overlay
}

func (s *providerState) overlayFetchFailure(data usage.UsageData, err error) usage.UsageData {
	message := "Provider fetch failed. Will retry."
	if usage.IsTimeout(err) {
		message = "Provider fetch timed out. Will retry."
	} else if errors.Is(err, context.Canceled) {
		message = "Provider fetch canceled."
	}
	if s.hasLastGood {
		data = overlayFetchFailureFields(copyUsageData(s.lastGood), data)
	} else {
		data = copyUsageData(data)
	}
	if data.ProviderName == "" {
		data.ProviderName = s.name
	}
	data.Error = &message
	return copyUsageData(data)
}

func overlayFetchFailureFields(base usage.UsageData, partial usage.UsageData) usage.UsageData {
	base.Auth = copyAuth(partial.Auth)
	base.NeedsReauth = partial.NeedsReauth
	base.ReauthCommand = copyString(partial.ReauthCommand)
	base.PrimaryStatusText = copyString(partial.PrimaryStatusText)
	base.SecondaryStatusText = copyString(partial.SecondaryStatusText)
	return base
}

func providerKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i int, j int) bool {
		return strings.ToLower(entries[i].Data.ProviderName) < strings.ToLower(entries[j].Data.ProviderName)
	})
}

type providerPanicError struct{}

func (providerPanicError) Error() string { return "provider panic" }
