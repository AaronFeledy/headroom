package cli

import (
	"strings"
	"testing"
	"time"
)

func TestExpiredMeterRetainsWarningUntilNewWindow(t *testing.T) {
	now := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	reset := now.Add(time.Minute)
	provider := Provider{ProviderName: "Codex", IsSuccess: true, Buckets: []Bucket{{ID: "session", Label: "5-Hour", Utilization: 92, ResetsAt: &reset}}}
	warnings := map[string]warningState{}
	render([]Provider{provider}, now, false, warnings)
	key := "codex\x00session"
	if warnings[key].level != 0 {
		t.Fatal("fixture did not begin at normal pace")
	}
	output := render([]Provider{provider}, reset.Add(time.Minute), false, warnings)
	if warnings[key].level != 0 || strings.Contains(output, "Critical") {
		t.Fatalf("expired reading created a new warning: %s", output)
	}
	next := reset.Add(5 * time.Hour)
	provider.Buckets[0].ResetsAt = &next
	render([]Provider{provider}, reset.Add(time.Minute), false, warnings)
	if warnings[key].level != 3 {
		t.Fatal("fresh window did not update warning")
	}
}

func TestProviderFailureRetainsWarningHysteresis(t *testing.T) {
	now := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	provider := Provider{ProviderName: "Claude", IsSuccess: true, Buckets: []Bucket{{ID: "session", Label: "Session", Utilization: 76}}}
	warnings := map[string]warningState{}
	render([]Provider{provider}, now, false, warnings)
	failure := "temporarily unavailable"
	render([]Provider{{ProviderName: "Claude", Error: &failure}}, now, false, warnings)
	provider.Buckets[0].Utilization = 72
	output := render([]Provider{provider}, now, false, warnings)
	if warnings["claude\x00session"].level != 2 || !strings.Contains(output, "Warning") {
		t.Fatalf("provider failure discarded warning hysteresis: %s", output)
	}
	render(nil, now, false, warnings)
	if len(warnings) != 0 {
		t.Fatal("removed providers kept stale warning state")
	}
}
