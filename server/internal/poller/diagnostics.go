package poller

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func diagnosticTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format(time.RFC3339Nano)
	return &value
}

func (s *providerState) diagnostics(entry Entry, failure *usage.FetchFailure) *usage.FetchDiagnostics {
	d := &usage.FetchDiagnostics{NextAttemptAt: s.nextDue.UTC().Format(time.RFC3339Nano)}
	if failure != nil {
		switch failure.Operation {
		case usage.OperationUsage, usage.OperationResetStatus, usage.OperationProfile:
			operation := string(failure.Operation)
			d.Operation = &operation
		}
		if failure.HTTPStatus >= 100 && failure.HTTPStatus <= 599 {
			status := failure.HTTPStatus
			d.HTTPStatus = &status
		}
		d.RetryAfter = diagnosticTime(failure.RetryAfter)
		if s.rateUntil.After(entry.FetchedAt) {
			d.CooldownUntil = diagnosticTime(s.rateUntil)
			source := "retry_after"
			if !failure.RetryAfter.After(entry.FetchedAt) || s.rateUntil.After(failure.RetryAfter) {
				source = "backoff"
			}
			d.CooldownSource = &source
		}
	}
	return d
}

func (s *providerState) logAttempt(ctx context.Context, entry Entry, failure *usage.FetchFailure) {
	if s.owner.logger == nil {
		return
	}
	provider := "Other"
	switch s.providerType {
	case "Claude", "Codex", "Cursor", "Grok", "OpenCodeGo":
		provider = s.providerType
	}
	outcome := "ok"
	if entry.Data.Error != nil {
		outcome = "failed"
	}
	if failure != nil {
		switch failure.Kind {
		case usage.FailureTransient, usage.FailureRateLimited, usage.FailureAuth, usage.FailureOther:
			outcome = string(failure.Kind)
		}
	}
	d := entry.Data.FetchStatus.Diagnostics
	attrs := []slog.Attr{
		slog.Int("pid", os.Getpid()),
		slog.String("provider", provider),
		slog.Bool("usage_success", entry.Data.Error == nil),
		slog.String("outcome", outcome),
		slog.String("fetched_at", entry.FetchedAt.UTC().Format(time.RFC3339Nano)),
		slog.String("next_attempt_at", d.NextAttemptAt),
	}
	if s.account != nil {
		attrs = append(attrs, slog.String("account", s.account.ID))
	}
	if d.Operation != nil {
		attrs = append(attrs, slog.String("operation", *d.Operation))
	}
	if d.HTTPStatus != nil {
		attrs = append(attrs, slog.Int("http_status", *d.HTTPStatus))
	}
	if d.RetryAfter != nil {
		attrs = append(attrs, slog.String("retry_after", *d.RetryAfter))
	}
	if d.CooldownUntil != nil {
		attrs = append(attrs, slog.String("cooldown_until", *d.CooldownUntil))
	}
	if d.CooldownSource != nil {
		attrs = append(attrs, slog.String("cooldown_source", *d.CooldownSource))
	}
	if s.hasPolicy() {
		attrs = append(attrs, slog.Duration("spacing", s.spacing), slog.Duration("idle", s.idle))
	}
	s.owner.logger.LogAttrs(ctx, slog.LevelInfo, "provider fetch", attrs...)
}
