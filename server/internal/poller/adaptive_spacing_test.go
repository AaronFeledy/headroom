package poller

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type adaptiveProvider struct {
	name      string
	policy    usage.PollPolicy
	responses []providerResult
	calls     atomic.Int32
	mu        sync.Mutex
}

func (p *adaptiveProvider) Name() string { return p.name }

func (p *adaptiveProvider) PollPolicy() usage.PollPolicy { return p.policy }

func (p *adaptiveProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	if err := ctx.Err(); err != nil {
		return usage.UsageData{ProviderName: p.name}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	index := int(p.calls.Add(1)) - 1
	if len(p.responses) == 0 {
		return usage.UsageData{ProviderName: p.name}, nil
	}
	if index >= len(p.responses) {
		index = len(p.responses) - 1
	}
	result := p.responses[index]
	if result.data.ProviderName == "" {
		result.data.ProviderName = p.name
	}
	return result.data, result.err
}

func adaptivePoller(t *testing.T, policy usage.PollPolicy) (*adaptiveProvider, *Poller, *advanceClock) {
	t.Helper()
	provider := &adaptiveProvider{name: "Claude", policy: policy}
	p, clock, _ := recoveryPoller(t, provider)
	return provider, p, clock
}

func percentReading(utilization float64) providerResult {
	return providerResult{data: usage.UsageData{Buckets: []usage.Bucket{{ID: "session", Utilization: utilization}}}}
}

func rateLimit(operation usage.FetchOperation, retryAfter time.Time, fallback bool) *usage.FetchFailure {
	return &usage.FetchFailure{Kind: usage.FailureRateLimited, Operation: operation, HTTPStatus: http.StatusTooManyRequests, RetryAfter: retryAfter, RateLimitFallback: fallback}
}

func pollProvider(t *testing.T, p *Poller, name string) Entry {
	t.Helper()
	entry, ok, err := p.PollProvider(context.Background(), name)
	if !ok || err != nil {
		t.Fatalf("PollProvider(%s) = %+v, %v", name, ok, err)
	}
	return entry
}

func policyState(t *testing.T, p *Poller) *providerState {
	t.Helper()
	states := p.enabledStates()
	if len(states) != 1 {
		t.Fatalf("states = %d, want 1", len(states))
	}
	return states[0]
}

func stateDurations(t *testing.T, p *Poller) (spacing, idle time.Duration, cleanStreak int) {
	t.Helper()
	state := policyState(t, p)
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.spacing, state.idle, state.cleanStreak
}

func nextDelay(t *testing.T, entry Entry) time.Duration {
	t.Helper()
	next, err := time.Parse(time.RFC3339Nano, entry.Data.FetchStatus.Diagnostics.NextAttemptAt)
	if err != nil {
		t.Fatalf("parse next_attempt_at: %v", err)
	}
	return next.Sub(entry.FetchedAt)
}

func Test_Adaptive_usage429_doubles_spacing_up_to_the_cap(t *testing.T) {
	policy := usage.PollPolicy{MinSpacing: 3 * time.Minute, MaxSpacing: 12 * time.Minute, IdleSpacing: 10 * time.Minute}
	limit := providerResult{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: rateLimit(usage.OperationUsage, time.Time{}, true)}}
	provider, p, clock := adaptivePoller(t, policy)
	provider.responses = []providerResult{limit, limit, limit, limit}
	for _, want := range []time.Duration{6 * time.Minute, 12 * time.Minute, 12 * time.Minute, 12 * time.Minute} {
		entry := pollProvider(t, p, "Claude")
		if entry.Data.Error == nil {
			t.Fatal("rate-limited attempt was reported as success")
		}
		spacing, _, streak := stateDurations(t, p)
		if spacing != want || streak != 0 {
			t.Fatalf("spacing = %s, streak = %d, want %s and 0", spacing, streak, want)
		}
		if delay := nextDelay(t, entry); delay != want {
			t.Fatalf("next delay = %s, want %s", delay, want)
		}
		clock.next(want)
	}
}

func Test_Adaptive_usage429_cooldown_is_new_spacing_without_usable_header(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter func(time.Time) time.Time
		fallback   bool
		want       time.Duration
		wantHeader bool
		wantSource string
	}{
		{"missing", func(time.Time) time.Time { return time.Time{} }, true, 6 * time.Minute, false, "backoff"},
		{"elapsed", func(base time.Time) time.Time { return base.Add(-time.Minute) }, false, 6 * time.Minute, true, "backoff"},
		{"future", func(base time.Time) time.Time { return base.Add(10 * time.Minute) }, false, 10 * time.Minute, true, "retry_after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, p, clock := adaptivePoller(t, claudePolicy)
			failure := rateLimit(usage.OperationUsage, tc.retryAfter(clock.Now()), tc.fallback)
			provider.responses = []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: failure}}}
			entry := pollProvider(t, p, "Claude")
			state := policyState(t, p)
			state.mu.RLock()
			rateUntil := state.rateUntil
			state.mu.RUnlock()
			wantUntil := entry.FetchedAt.Add(tc.want)
			if !rateUntil.Equal(wantUntil) {
				t.Fatalf("rateUntil = %s, want %s", rateUntil, wantUntil)
			}
			if spacing, _, _ := stateDurations(t, p); spacing != 6*time.Minute {
				t.Fatalf("spacing = %s, want 6m", spacing)
			}
			d := entry.Data.FetchStatus.Diagnostics
			if d.RetryAfter != nil != tc.wantHeader {
				t.Fatalf("retry_after present = %t, want %t", d.RetryAfter != nil, tc.wantHeader)
			}
			cooldown, err := time.Parse(time.RFC3339Nano, *d.CooldownUntil)
			if err != nil || !cooldown.Equal(wantUntil) {
				t.Fatalf("cooldown_until = %v, want %s", d.CooldownUntil, wantUntil)
			}
			if d.CooldownSource == nil || *d.CooldownSource != tc.wantSource {
				t.Fatalf("cooldown_source = %v, want %s", d.CooldownSource, tc.wantSource)
			}
			if delay := nextDelay(t, entry); delay != tc.want {
				t.Fatalf("next delay = %s, want %s", delay, tc.want)
			}
		})
	}
}

func Test_Adaptive_clean_streak_steps_spacing_down_to_the_floor(t *testing.T) {
	provider, p, clock := adaptivePoller(t, claudePolicy)
	responses := []providerResult{{data: usage.UsageData{Error: strPtr("limited"), FetchFailure: rateLimit(usage.OperationUsage, time.Time{}, true)}}}
	for i := 0; i < 12; i++ {
		responses = append(responses, percentReading(float64(10+i)))
	}
	provider.responses = responses
	pollProvider(t, p, "Claude")
	if spacing, _, _ := stateDurations(t, p); spacing != 6*time.Minute {
		t.Fatalf("spacing after 429 = %s, want 6m", spacing)
	}
	checkpoints := map[int]time.Duration{4: 4*time.Minute + 30*time.Second, 8: 3*time.Minute + 22*time.Second + 500*time.Millisecond, 12: 3 * time.Minute}
	for i := 0; i < 12; i++ {
		spacing, _, _ := stateDurations(t, p)
		clock.next(spacing)
		pollProvider(t, p, "Claude")
		if want, ok := checkpoints[i+1]; ok {
			got, _, streak := stateDurations(t, p)
			if got != want || streak != 0 {
				t.Fatalf("after %d clean responses: spacing = %s, streak = %d, want %s and 0", i+1, got, streak, want)
			}
		}
	}
}

func Test_Adaptive_http_transient_failure_keeps_the_real_next_attempt(t *testing.T) {
	provider, p, _ := adaptivePoller(t, claudePolicy)
	provider.responses = []providerResult{{data: usage.UsageData{Error: strPtr("outage"), FetchFailure: &usage.FetchFailure{Kind: usage.FailureTransient, Operation: usage.OperationUsage, HTTPStatus: 500}}}}
	entry := pollProvider(t, p, "Claude")
	if delay := nextDelay(t, entry); delay != 3*time.Minute {
		t.Fatalf("next delay = %s, want the spacing window", delay)
	}
}

func Test_Adaptive_auxiliary_429_leaves_spacing_unchanged(t *testing.T) {
	provider, p, _ := adaptivePoller(t, claudePolicy)
	reading := percentReading(20).data
	reading.FetchFailure = rateLimit(usage.OperationProfile, time.Time{}, true)
	provider.responses = []providerResult{{data: reading}}
	entry := pollProvider(t, p, "Claude")
	if entry.Data.Error != nil {
		t.Fatal("auxiliary throttling degraded successful usage")
	}
	spacing, _, _ := stateDurations(t, p)
	if spacing != 3*time.Minute {
		t.Fatalf("spacing = %s, want the 3m minimum", spacing)
	}
	state := policyState(t, p)
	state.mu.RLock()
	rateUntil := state.rateUntil
	state.mu.RUnlock()
	if !rateUntil.Equal(entry.FetchedAt.Add(3 * time.Minute)) {
		t.Fatalf("auxiliary cooldown = %s, want the legacy spacing-sized backoff", rateUntil.Sub(entry.FetchedAt))
	}
	if d := entry.Data.FetchStatus.Diagnostics; *d.Operation != "profile" || *d.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("auxiliary diagnostics = %+v", d)
	}
}
