package poller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func Test_Grok_partial_success_preserves_rate_cooldown_for_all_dispatch_paths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses grokResponses
		cooldown  time.Duration
	}{
		{"CLI success browser limited", grokResponses{cliStatus: 200, webStatus: 429, webRetry: 2 * time.Minute}, 2 * time.Minute},
		{"CLI limited browser success", grokResponses{cliStatus: 429, webStatus: 200, cliRetry: 2 * time.Minute}, 2 * time.Minute},
		{"CLI success browser fallback limit", grokResponses{cliStatus: 200, webStatus: 429}, time.Minute},
		{"CLI fallback limit browser success", grokResponses{cliStatus: 429, webStatus: 200}, time.Minute},
	} {
		for _, path := range []string{"scheduled", "manual", "synchronous"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				// Given
				var clock *advanceClock
				provider, counts := grokRecoveryProvider(t, func() time.Time { return clock.Now() }, tc.responses)
				p, assignedClock, _ := recoveryPoller(t, provider)
				clock = assignedClock
				p.dispatchDue(p.enabledStates())
				settle(p)
				entry, _ := p.Get("Grok")
				encoded, err := json.Marshal(entry.Data)
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Error       *string `json:"error"`
					IsSuccess   bool    `json:"is_success"`
					FetchStatus struct {
						FailureKind *string `json:"failure_kind"`
					} `json:"fetch_status"`
				}
				if err := json.Unmarshal(encoded, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Error != nil || !wire.IsSuccess || wire.FetchStatus.FailureKind != nil {
					t.Fatalf("partial success wire = %s", encoded)
				}
				before := counts.total.Load()
				clock.next(tc.cooldown - time.Second)
				// When
				switch path {
				case "scheduled":
					p.dispatchDue(p.enabledStates())
					settle(p)
				case "manual":
					if _, err := p.RequestRefresh(); !errors.Is(err, ErrRefreshRateLimited) {
						t.Errorf("refresh before rate deadline: %v", err)
					}
					settle(p)
				case "synchronous":
					p.PollProvider(context.Background(), "Grok")
				}
				// Then
				if counts.total.Load() != before {
					t.Fatalf("%s bypassed partial-success rate limit: calls %d -> %d", path, before, counts.total.Load())
				}
			})
		}
	}
}

func Test_Grok_multiple_rate_limits_keep_longest_deadline_including_fallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses grokResponses
		cooldown  time.Duration
	}{
		{"longer CLI deadline", grokResponses{cliStatus: 429, webStatus: 429, cliRetry: 3 * time.Minute, webRetry: 2 * time.Minute}, 3 * time.Minute},
		{"fallback exceeds valid deadline", grokResponses{cliStatus: 429, webStatus: 429, cliRetry: 30 * time.Second}, time.Minute},
		{"fallback precedes valid deadline", grokResponses{cliStatus: 429, webStatus: 429, webRetry: 30 * time.Second}, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			var clock *advanceClock
			provider, counts := grokRecoveryProvider(t, func() time.Time { return clock.Now() }, tc.responses)
			p, assignedClock, _ := recoveryPoller(t, provider)
			clock = assignedClock
			p.dispatchDue(p.enabledStates())
			settle(p)
			before := counts.total.Load()
			clock.next(tc.cooldown - time.Second)
			// When
			p.PollProvider(context.Background(), "Grok")
			// Then
			if counts.total.Load() != before {
				t.Fatal("combined rate deadline was shortened")
			}
		})
	}
}

func Test_Grok_partial_success_rate_fallback_keeps_doubling_backoff(t *testing.T) {
	// Given
	var clock *advanceClock
	provider, counts := grokRecoveryProvider(t, func() time.Time { return clock.Now() }, grokResponses{cliStatus: 200, webStatus: 429})
	p, assignedClock, _ := recoveryPoller(t, provider)
	clock = assignedClock
	p.dispatchDue(p.enabledStates())
	settle(p)
	clock.next(time.Minute)
	p.dispatchDue(p.enabledStates())
	settle(p)
	before := counts.total.Load()
	clock.next(119 * time.Second)
	// When
	p.dispatchDue(p.enabledStates())
	settle(p)
	// Then
	if counts.total.Load() != before {
		t.Fatal("wire success reset rate-limit fallback backoff")
	}
}
