package poller

import (
	"errors"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func statusReading(utilization float64) providerResult {
	return providerResult{data: usage.UsageData{Buckets: []usage.Bucket{{ID: "extra", Utilization: utilization, Measurement: usage.MeasurementStatus}}}}
}

func Test_Adaptive_idle_grows_to_the_cap_on_unchanged_readings(t *testing.T) {
	provider, p, clock := adaptivePoller(t, claudePolicy)
	for i := 0; i < 5; i++ {
		provider.responses = append(provider.responses, percentReading(20))
	}
	wantIdle := []time.Duration{0, 4*time.Minute + 30*time.Second, 6*time.Minute + 45*time.Second, 10 * time.Minute, 10 * time.Minute}
	wantDelay := []time.Duration{3 * time.Minute, 4*time.Minute + 30*time.Second, 6*time.Minute + 45*time.Second, 10 * time.Minute, 10 * time.Minute}
	for i := 0; i < 5; i++ {
		if i > 0 {
			clock.next(3 * time.Minute)
		}
		entry := pollProvider(t, p, "Claude")
		_, idle, _ := stateDurations(t, p)
		if idle != wantIdle[i] {
			t.Fatalf("idle after %d unchanged readings = %s, want %s", i+1, idle, wantIdle[i])
		}
		if delay := nextDelay(t, entry); delay != wantDelay[i] {
			t.Fatalf("next delay after %d readings = %s, want %s", i+1, delay, wantDelay[i])
		}
	}
}

func Test_Adaptive_idle_resets_on_change_failure_and_near_limit(t *testing.T) {
	t.Run("changed reading", func(t *testing.T) {
		provider, p, clock := adaptivePoller(t, claudePolicy)
		provider.responses = []providerResult{percentReading(10), percentReading(10), percentReading(11)}
		pollProvider(t, p, "Claude")
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 4*time.Minute+30*time.Second {
			t.Fatalf("idle did not grow: %s", idle)
		}
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 0 {
			t.Fatalf("idle after changed reading = %s, want 0", idle)
		}
	})
	t.Run("failed attempt", func(t *testing.T) {
		provider, p, clock := adaptivePoller(t, claudePolicy)
		provider.responses = []providerResult{percentReading(10), percentReading(10), {err: usage.TransportFailure(errors.New("dial failed"))}}
		pollProvider(t, p, "Claude")
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 4*time.Minute+30*time.Second {
			t.Fatalf("idle did not grow: %s", idle)
		}
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 0 {
			t.Fatalf("idle after failure = %s, want 0", idle)
		}
	})
	t.Run("percent meter at ninety", func(t *testing.T) {
		provider, p, clock := adaptivePoller(t, claudePolicy)
		provider.responses = []providerResult{percentReading(85), percentReading(92), percentReading(92)}
		pollProvider(t, p, "Claude")
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 0 {
			t.Fatalf("idle near the limit = %s, want 0", idle)
		}
	})
	t.Run("status-only meter ignores ninety", func(t *testing.T) {
		provider, p, clock := adaptivePoller(t, claudePolicy)
		provider.responses = []providerResult{statusReading(95), statusReading(95)}
		pollProvider(t, p, "Claude")
		clock.next(3 * time.Minute)
		pollProvider(t, p, "Claude")
		if _, idle, _ := stateDurations(t, p); idle != 4*time.Minute+30*time.Second {
			t.Fatalf("status meter suppressed idle growth: %s", idle)
		}
	})
}

func Test_Adaptive_forced_polls_ignore_idle_but_honor_spacing(t *testing.T) {
	provider, p, clock := adaptivePoller(t, claudePolicy)
	provider.responses = []providerResult{percentReading(10), percentReading(10), percentReading(11)}
	pollProvider(t, p, "Claude")
	clock.next(3 * time.Minute)
	pollProvider(t, p, "Claude")
	if _, idle, _ := stateDurations(t, p); idle <= 3*time.Minute {
		t.Fatalf("idle did not grow past spacing: %s", idle)
	}
	clock.next(3 * time.Minute)
	p.dispatchDue(p.enabledStates())
	settle(p)
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("scheduler ignored idle: calls = %d", got)
	}
	pollProvider(t, p, "Claude")
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("forced poll honored idle: calls = %d", got)
	}
	clock.next(time.Minute)
	pollProvider(t, p, "Claude")
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("forced poll bypassed spacing: calls = %d", got)
	}
}
