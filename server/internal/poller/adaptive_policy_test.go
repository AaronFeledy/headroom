package poller

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func Test_Adaptive_provider_without_policy_is_unaffected(t *testing.T) {
	provider := &sequenceProvider{name: "Other", responses: []providerResult{percentReading(10), percentReading(10)}}
	p, clock, _ := recoveryPoller(t, provider)
	pollProvider(t, p, "Other")
	clock.next(time.Minute)
	entry := pollProvider(t, p, "Other")
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("unchanged reading delayed a stateless provider: calls = %d", got)
	}
	state := policyState(t, p)
	state.mu.RLock()
	spacing, idle := state.spacing, state.idle
	state.mu.RUnlock()
	if spacing != 0 || idle != 0 {
		t.Fatalf("stateless provider adapted: spacing = %s, idle = %s", spacing, idle)
	}
	if delay := nextDelay(t, entry); delay != time.Minute {
		t.Fatalf("next delay = %s, want the configured minute", delay)
	}
}

func Test_Adaptive_logs_spacing_and_idle_only_with_a_policy(t *testing.T) {
	provider, p, _ := adaptivePoller(t, claudePolicy)
	provider.responses = []providerResult{percentReading(10)}
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	pollProvider(t, p, "Claude")
	if logged := logs.String(); !strings.Contains(logged, `"spacing":180000000000`) || !strings.Contains(logged, `"idle":0`) {
		t.Fatalf("missing adaptive attributes: %s", logged)
	}

	plain := &sequenceProvider{name: "Other", responses: []providerResult{percentReading(10)}}
	other, _, _ := recoveryPoller(t, plain)
	var plainLogs bytes.Buffer
	other.logger = slog.New(slog.NewJSONHandler(&plainLogs, nil))
	pollProvider(t, other, "Other")
	if strings.Contains(plainLogs.String(), `"spacing"`) || strings.Contains(plainLogs.String(), `"idle"`) {
		t.Fatalf("stateless provider logged adaptive attributes: %s", plainLogs.String())
	}
}
