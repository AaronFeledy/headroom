package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMeasurementControlsBarsPaceAndRetiredWarnings(t *testing.T) {
	for _, measurement := range []string{`"status"`, `"future"`, `false`, `{}`, `""`} {
		t.Run(measurement, func(t *testing.T) {
			// Given
			body := []byte(`[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"weekly","label":"Wallet","utilization":99,"measurement":` + measurement + `,"status_text":"Enabled","detail_text":"Read only","resets_at":"2030-01-01T00:00:00Z"}]}]`)
			providers, err := decodeUsage(body)
			if err != nil {
				t.Fatal(err)
			}
			warnings := map[string]warningState{"cursor\x00weekly": {level: 3}}
			// When
			text := render(providers, time.Now(), false, warnings)
			// Then
			if strings.Contains(text, "%") || strings.Contains(text, "Normal") || !strings.Contains(text, "Wallet: Enabled") || !strings.Contains(text, "Read only") || len(warnings) != 0 {
				t.Fatalf("status meter rendered: %s %#v", text, warnings)
			}
			if pacing("Cursor", providers[0].Buckets[0], time.Now()).available {
				t.Fatal("status has pace")
			}
		})
	}
}

func TestLegacyStatusAndExplicitMeasuredZero(t *testing.T) {
	for _, measurement := range []string{`null`, `"percent"`} {
		// Given
		body := []byte(`[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,"secondary_status_text":"Enabled","buckets":[{"id":"on_demand","label":"Extra","utilization":0,"measurement":` + measurement + `,"resets_at":null}]}]`)
		providers, err := decodeUsage(body)
		if err != nil {
			t.Fatal(err)
		}
		// When
		text := render(providers, time.Now(), false, map[string]warningState{})
		// Then
		if strings.Contains(text, "0.0% used") != (measurement == `"percent"`) {
			t.Fatal(text)
		}
	}
}

func TestWeeklyReportedCursorWindowAndRawPreservationAgree(t *testing.T) {
	// Given
	body := []byte(`[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,"legacy":"keep","usage_details":{"future":42},"buckets":[
	{"id":"auto","label":"Auto","utilization":0,"starts_at":"2026-10-01T00:00:00Z","resets_at":"2026-10-08T00:00:00Z","measurement":"percent","future":7},
	{"id":"plan","label":"Plan","utilization":0,"starts_at":"2026-10-01T00:00:00Z","resets_at":"2026-11-01T00:00:00Z"},
	{"id":"api","label":"API","utilization":0,"resets_at":"2026-10-08T00:00:00Z"},
	{"id":"weekly_grok_bot","label":"Grok Bot","utilization":0,"resets_at":null}]}]`)
	options, _, _ := baseOptions(body)
	// When
	snapshot, providers, err := fetchUsage(context.Background(), options.defaults(), dashboardFlags{weeklyOnly: true})
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(providers[0].Buckets) != 2 || providers[0].Buckets[0].ID != "auto" {
		t.Fatal("typed filter omitted reported week")
	}
	var raw []struct {
		Buckets []Bucket `json:"buckets"`
	}
	if err := json.Unmarshal(snapshot.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw[0].Buckets) != 2 || raw[0].Buckets[0].ID != "auto" || !bytes.Contains(snapshot.Body, []byte(`"future":7`)) || !bytes.Contains(snapshot.Body, []byte(`"usage_details"`)) || !bytes.Contains(snapshot.Body, []byte(`"legacy":"keep"`)) {
		t.Fatal(string(snapshot.Body))
	}
}

func TestExactMinorUnitsAndTerminalBounds(t *testing.T) {
	// Given
	providers, err := decodeUsage(detailSnapshot(`{"finances":{"state":"ok","items":[{"currency":"USD","prepaid_remaining_minor":9007199254740991}]},"plan":{"state":"ok","label":"a\u001b\n\u202eb"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// When
	text := renderDetails(providers[0].UsageDetails)
	// Then
	if !strings.Contains(text, "$90071992547409.91") || strings.ContainsAny(text, "\x1b\u202e") {
		t.Fatal(text)
	}
	if len([]rune(terminalText(strings.Repeat("a ", 100)))) > 80 {
		t.Fatal("unbounded label")
	}
}
