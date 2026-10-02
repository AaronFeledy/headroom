package cli

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPacingUsesReportedPeriod(t *testing.T) {
	now := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	for _, provider := range []string{"Cursor", "FutureProvider"} {
		body := `[{"provider_name":"PROVIDER","error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"plan","label":"Plan","utilization":50,"starts_at":"2026-02-01T00:00:00Z","resets_at":"2026-03-01T00:00:00Z"}]}]`
		providers, err := decodeUsage([]byte(strings.Replace(body, "PROVIDER", provider, 1)))
		if err != nil {
			t.Fatal(err)
		}
		got := pacing(provider, providers[0].Buckets[0], now)
		if !got.available || math.Abs(got.expected-50) > 1e-9 || got.duration != 28*24*time.Hour || got.step != 7*24*time.Hour {
			t.Fatalf("%s: pacing = %+v, want midpoint of reported 28-day cycle", provider, got)
		}
	}
}

func TestPacingIgnoresInvalidOptionalPeriodStart(t *testing.T) {
	now := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	for _, start := range []string{`null`, `"bad-date"`, `{}`, `42`, `"2026-03-01T00:00:00Z"`, `"2026-03-02T00:00:00Z"`, `"2024-01-01T00:00:00Z"`} {
		body := `[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"plan","label":"Plan","utilization":50,"starts_at":START,"resets_at":"2026-03-01T00:00:00Z"}]}]`
		providers, err := decodeUsage([]byte(strings.Replace(body, "START", start, 1)))
		if err != nil {
			t.Fatalf("optional start %s invalidated usage: %v", start, err)
		}
		got := pacing("Cursor", providers[0].Buckets[0], now)
		if !got.available || got.duration != 30*24*time.Hour {
			t.Fatalf("optional start %s: pacing = %+v, want fallback", start, got)
		}
	}
}
