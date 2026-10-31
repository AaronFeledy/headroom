package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const richFixture = `{"plan":{"state":"ok","label":"Pro","is_unlimited":false,"scope":{"kind":"personal","shared_pool":false}},
"quotas":[{"state":"ok","bucket_id":"weekly_grok_bot","plan_label":"Grok Bot","has_available_usage":true,"scope":{"kind":"team","shared_pool":true}}],
"finances":{"state":"ok","items":[{"currency":"USD","starts_at":"2026-10-01T00:00:00Z","resets_at":"2026-11-01T00:00:00Z","prepaid_remaining_minor":1234,"included_used_minor":0,"included_limit_minor":2000,"included_remaining_minor":2000,"extra_used_minor":125,"extra_cap_minor":500,"extra_enabled":false,"auto_top_up_enabled":true}]},
"credit_grants":{"state":"ok","items":[{"display_name":"Restricted grant","product":"Grok Bot","currency":"USD","remaining_minor":100,"total_minor":200,"expires_at":"2025-01-01T00:00:00Z","scope":{"kind":"unknown","shared_pool":null}}]},
"reset_inventory":{"state":"ok","reported_count":3,"valid_now_count":0,"windows":[{"count":3,"valid_from":"2026-10-01T00:00:00Z","expires_at":"2026-11-01T00:00:00Z"}]},
"product_usage":{"state":"ok","parent_bucket_id":"weekly_grok_bot","items":[{"product":"Editor","reported_percent":25},{"product":"Agent","reported_percent":10}]}}`

func detailSnapshot(details string) []byte {
	return []byte(`[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,"future":{"keep":42},"current":{"utilization":12},"buckets":[{"id":"auto","label":"Auto","utilization":25,"resets_at":null,"measurement":"percent"}],"usage_details":` + details + `}]`)
}

func TestDetailsRenderSeparateReadOnlyFacts(t *testing.T) {
	// Given
	providers, err := decodeUsage(detailSnapshot(richFixture))
	if err != nil {
		t.Fatal(err)
	}
	// When
	text := render(providers, time.Unix(0, 0), false, map[string]warningState{})
	// Then
	expiry := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
	for _, want := range []string{"Prepaid balance: $12.34", "Included spend: spent $0.00", "limit $20.00", "Extra usage: used $1.25", "cap $5.00", "Restricted grant", "expires " + expiry, "3 reported", "0 in valid windows now", "Reset window", "25.0% reported contribution", "10.0% reported contribution", "Quota weekly_grok_bot", "shared pool"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Count(text, "% used") != 1 || strings.Contains(text, "banked") {
		t.Fatalf("metadata became meters/actions: %s", text)
	}
}

func TestOptionalMalformedDetailsDoNotRejectMeters(t *testing.T) {
	for _, details := range []string{`null`, `17`, `{"plan":"bad"}`, `{"finances":{"state":"ok","items":"bad"}}`,
		`{"finances":{"state":"ok","items":[{"currency":"USD","prepaid_remaining_minor":1.1}]}}`,
		`{"finances":{"state":"ok","items":[{"currency":"USD","prepaid_remaining_minor":9007199254740992}]}}`,
		`{"product_usage":{"state":"ok","items":[{"product":"bad","reported_percent":1e999}]}}`} {
		t.Run(details, func(t *testing.T) {
			// Given / When
			providers, err := decodeUsage(detailSnapshot(details))
			// Then
			if err != nil || len(providers[0].Buckets) != 1 {
				t.Fatalf("optional metadata rejected snapshot: %v", err)
			}
			text := renderDetails(providers[0].UsageDetails)
			if strings.Contains(text, "$0") || strings.Contains(text, "Inf") {
				t.Fatalf("invented metadata: %s", text)
			}
		})
	}
}

func TestDetailsStateAndNullSemantics(t *testing.T) {
	for _, state := range []string{"unknown", "unsupported", "stale"} {
		// Given
		body := detailSnapshot(strings.ReplaceAll(richFixture, `"ok"`, fmt.Sprintf("%q", state)))
		// When
		providers, err := decodeUsage(body)
		// Then
		if err != nil {
			t.Fatal(err)
		}
		if text := renderDetails(providers[0].UsageDetails); text != "" {
			t.Fatalf("unobserved %s rendered: %s", state, text)
		}
	}
	// Given
	providers, err := decodeUsage(detailSnapshot(`{"quotas":[{"state":"stale","observed_at":"2025-01-01T00:00:00Z","bucket_id":"weekly_grok_bot","has_available_usage":false}],"credit_grants":{"state":"ok","items":[]},"reset_inventory":{"state":"ok","reported_count":0,"windows":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	// When
	text := renderDetails(providers[0].UsageDetails)
	// Then
	observed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Local().Format("2006-01-02")
	if !strings.Contains(text, "at observation: no") || !strings.Contains(text, observed) || !strings.Contains(text, "0 reported") || strings.Contains(text, "Grant") {
		t.Fatal(text)
	}
}

func TestDetailsClearOnProviderError(t *testing.T) {
	// Given
	body := bytes.Replace(detailSnapshot(richFixture), []byte(`"error":null,"is_success":true`), []byte(`"error":"offline","is_success":false`), 1)
	// When
	providers, err := decodeUsage(body)
	// Then
	if err != nil || providers[0].UsageDetails != nil || strings.Contains(render(providers, time.Now(), false, map[string]warningState{}), "$12.34") {
		t.Fatalf("retained details after error: %v", err)
	}
}

func TestMalformedSectionRetainsKnownSiblingAndEmptyListsStayQuiet(t *testing.T) {
	// Given
	providers, err := decodeUsage(detailSnapshot(`{"plan":{"state":"ok","label":"Known plan"},"finances":{"state":"ok","items":[{"currency":"USD","prepaid_remaining_minor":-1}]},"credit_grants":{"state":"ok","items":[]},"product_usage":{"state":"ok","items":null},"reset_inventory":{"state":"ok","windows":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	// When
	text := renderDetails(providers[0].UsageDetails)
	// Then
	if text != "  Plan: Known plan\n" {
		t.Fatal(text)
	}
}

func TestDetailListBoundsAndHostileLabels(t *testing.T) {
	// Given
	items := strings.TrimSuffix(strings.Repeat(`{"display_name":"hidden","currency":"USD","remaining_minor":1},`, 65), ",")
	body := detailSnapshot(`{"credit_grants":{"state":"ok","items":[` + items + `]},"plan":{"state":"ok","label":"` + strings.Repeat("x", 150) + `\u001b\u202e"}}`)
	providers, err := decodeUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	// When
	text := renderDetails(providers[0].UsageDetails)
	// Then
	if strings.Contains(text, "hidden") || strings.ContainsAny(text, "\x1b\u202e") {
		t.Fatal(text)
	}
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > 80 {
			t.Fatalf("rich row exceeds terminal width: %q", line)
		}
	}
}

func TestOnceAndJSONReadSyntheticHTTPWithoutLosingFields(t *testing.T) {
	// Given
	body := detailSnapshot(richFixture)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/usage" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	for _, mode := range []string{"--once", "--json"} {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			options := Options{Args: []string{mode, "--url", server.URL}, Env: []string{}, Stdout: &stdout, Stderr: &stderr, HTTPClient: server.Client()}
			// When
			err := Run(context.Background(), options)
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if mode == "--json" {
				if !bytes.Equal(bytes.TrimSpace(stdout.Bytes()), body) || !json.Valid(stdout.Bytes()) {
					t.Fatal("raw JSON changed")
				}
			} else if !strings.Contains(stdout.String(), "Prepaid balance: $12.34") {
				t.Fatal(stdout.String())
			}
		})
	}
}
