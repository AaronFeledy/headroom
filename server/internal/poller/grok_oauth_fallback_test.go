package poller

import (
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Grok_terminal_OAuth_failure_keeps_normal_cadence_after_browser_fallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses grokResponses
		kind      usage.FailureKind
		success   bool
	}{
		{"OAuth other browser transient", grokResponses{cliStatus: 200, tokenStatus: 503, webStatus: 503}, usage.FailureOther, false},
		{"OAuth auth browser transient", grokResponses{cliStatus: 200, tokenStatus: 400, webStatus: 503}, usage.FailureAuth, false},
		{"OAuth other browser success", grokResponses{cliStatus: 200, tokenStatus: 503, webStatus: 200}, usage.FailureOther, true},
		{"OAuth auth browser success", grokResponses{cliStatus: 200, tokenStatus: 400, webStatus: 200}, usage.FailureAuth, true},
	} {
		for _, elapsed := range []time.Duration{59 * time.Second, time.Minute} {
			t.Run(tc.name+"/"+elapsed.String(), func(t *testing.T) {
				// Given
				var clock *advanceClock
				provider, counts := grokRecoveryProvider(t, func() time.Time { return clock.Now() }, tc.responses)
				p, assignedClock, _ := recoveryPoller(t, provider)
				clock = assignedClock
				p.dispatchDue(p.enabledStates())
				settle(p)
				entry, _ := p.Get("Grok")
				if (entry.Data.Error == nil) != tc.success {
					t.Fatal("browser fallback changed wire outcome")
				}
				if entry.Data.FetchFailure == nil || entry.Data.FetchFailure.Kind != tc.kind {
					t.Errorf("terminal OAuth classification = %+v, want %s", entry.Data.FetchFailure, tc.kind)
				}
				if tc.success && entry.Data.FetchStatus.FailureKind != nil {
					t.Fatal("successful fallback acquired wire failure kind")
				}
				if !tc.success && (entry.Data.FetchStatus.FailureKind == nil || *entry.Data.FetchStatus.FailureKind != tc.kind) {
					t.Fatal("secondary browser failure overrode terminal wire classification")
				}
				before := counts.token.Load()
				clock.next(elapsed)
				// When
				p.dispatchDue(p.enabledStates())
				settle(p)
				// Then
				want := before
				if elapsed == time.Minute {
					want++
				}
				if counts.token.Load() != want {
					t.Fatalf("OAuth calls at %s = %d, want %d", elapsed, counts.token.Load(), want)
				}
			})
		}
	}
}

func Test_Grok_terminal_OAuth_failure_preserves_secondary_rate_deadline(t *testing.T) {
	// Given
	var clock *advanceClock
	provider, counts := grokRecoveryProvider(t, func() time.Time { return clock.Now() }, grokResponses{cliStatus: 200, tokenStatus: 503, webStatus: 429, webRetry: 2 * time.Minute})
	p, assignedClock, _ := recoveryPoller(t, provider)
	clock = assignedClock
	p.dispatchDue(p.enabledStates())
	settle(p)
	entry, _ := p.Get("Grok")
	if entry.Data.FetchFailure == nil || entry.Data.FetchFailure.Kind != usage.FailureOther || entry.Data.FetchStatus.FailureKind == nil || *entry.Data.FetchStatus.FailureKind != usage.FailureOther {
		t.Fatal("secondary rate limit replaced terminal OAuth classification")
	}
	before := counts.total.Load()
	clock.next(119 * time.Second)
	// When
	p.dispatchDue(p.enabledStates())
	settle(p)
	// Then
	if counts.total.Load() != before {
		t.Fatal("terminal OAuth classification discarded browser rate deadline")
	}
}
