package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Diagnostics_logs_once_per_attempt_not_cached_read(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Claude"}
	p, clock, _ := recoveryPoller(t, provider)
	provider.responses = []providerResult{{data: usage.UsageData{FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(2 * time.Minute)}}}, {}}
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, _, err := p.PollProvider(context.Background(), "Claude"); err != nil {
		t.Fatal(err)
	}
	// When
	p.Get("Claude")
	p.Snapshot()
	if _, _, err := p.PollProvider(context.Background(), "Claude"); err != nil {
		t.Fatal(err)
	}
	// Then
	if got := strings.Count(logs.String(), `"msg":"provider fetch"`); got != 1 {
		t.Fatalf("cached reads changed attempt log count: got %d, want 1", got)
	}
	// When
	clock.next(2 * time.Minute)
	if _, _, err := p.PollProvider(context.Background(), "Claude"); err != nil {
		t.Fatal(err)
	}
	// Then
	if got := strings.Count(logs.String(), `"msg":"provider fetch"`); got != 2 {
		t.Fatalf("new attempt log count: got %d, want 2", got)
	}
}

func Test_Diagnostics_auxiliary_cooldown_preserves_success_and_copies(t *testing.T) {
	provider := &sequenceProvider{name: "Claude"}
	p, clock, _ := recoveryPoller(t, provider)
	failure := &usage.FetchFailure{Kind: usage.FailureRateLimited, Operation: usage.OperationProfile, HTTPStatus: 429, RetryAfter: clock.Now().Add(2 * time.Minute)}
	provider.responses = []providerResult{{data: usage.UsageData{FetchFailure: failure, ProviderAccountID: "secret-account"}}}
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	p.dispatchDue(p.enabledStates())
	settle(p)
	entry, _ := p.Get("Claude")
	d := entry.Data.FetchStatus.Diagnostics
	if entry.Data.Error != nil || entry.Data.FetchStatus.FailureKind != nil || d == nil || *d.Operation != "profile" || *d.HTTPStatus != 429 || *d.CooldownSource != "retry_after" {
		t.Fatalf("wrong diagnostics: %+v", d)
	}
	if d.NextAttemptAt != clock.Now().Add(2*time.Minute).Format(time.RFC3339Nano) || *d.CooldownUntil != d.NextAttemptAt {
		t.Fatal("schedule not reflected")
	}
	*d.Operation = "changed"
	*d.HTTPStatus = 500
	*d.RetryAfter = "changed"
	*d.CooldownUntil = "changed"
	*d.CooldownSource = "changed"
	again, _ := p.Get("Claude")
	a := again.Data.FetchStatus.Diagnostics
	if *a.Operation != "profile" || *a.HTTPStatus != 429 || *a.RetryAfter == "changed" || *a.CooldownUntil == "changed" || *a.CooldownSource != "retry_after" {
		t.Fatal("snapshot diagnostics alias poller state")
	}
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), `"http_status":429`) || !strings.Contains(logs.String(), `"usage_success":true`) || !strings.Contains(logs.String(), `"pid":`) {
		t.Fatal("missing safe log or leaked account")
	}
}

func Test_Diagnostics_rejects_untrusted_labels_and_statuses(t *testing.T) {
	failure := &usage.FetchFailure{Kind: usage.FailureRateLimited, Operation: "secret-url", HTTPStatus: 999, RetryAfter: time.Time{}}
	provider := &sequenceProvider{name: "secret-provider", responses: []providerResult{{data: usage.UsageData{Error: strPtr("secret-body"), FetchFailure: failure, CredentialEpoch: strPtr("secret-epoch")}}}}
	p, _, _ := recoveryPoller(t, provider)
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	p.dispatchDue(p.enabledStates())
	settle(p)
	entry, _ := p.Get("secret-provider")
	encoded, _ := json.Marshal(entry.Data.FetchStatus.Diagnostics)
	if strings.Contains(string(encoded), "secret") || strings.Contains(logs.String(), "secret") || entry.Data.FetchStatus.Diagnostics.HTTPStatus != nil {
		t.Fatal("diagnostic summary accepted untrusted values")
	}
	if *entry.Data.FetchStatus.Diagnostics.CooldownSource != "backoff" {
		t.Fatal("missing fallback source")
	}
}

func Test_Diagnostics_cooldown_source_reports_winning_deadline(t *testing.T) {
	for _, tc := range []struct {
		deadline time.Duration
		source   string
	}{{30 * time.Second, "backoff"}, {3 * time.Minute, "retry_after"}} {
		t.Run(tc.source, func(t *testing.T) {
			provider := &sequenceProvider{name: "Grok"}
			p, clock, _ := recoveryPoller(t, provider)
			provider.responses = []providerResult{{data: usage.UsageData{FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: clock.Now().Add(tc.deadline), RateLimitFallback: true}}}}
			p.dispatchDue(p.enabledStates())
			settle(p)
			entry, _ := p.Get("Grok")
			if *entry.Data.FetchStatus.Diagnostics.CooldownSource != tc.source {
				t.Fatal("diagnostics did not identify the winning deadline")
			}
		})
	}
}
