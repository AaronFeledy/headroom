package poller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestCopyUsageDataPreservesDetailsAndMeasurementIndependently(t *testing.T) {
	// Given
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	amount, shared := int64(500), true
	source := usage.UsageData{
		Buckets: []usage.Bucket{{ID: "plan", Measurement: usage.MeasurementStatus}},
		UsageDetails: &usage.UsageDetails{
			Finances: &usage.FinancialDetails{
				Observation: usage.Observation{State: usage.DetailOK, ObservedAt: &now},
				Items: []usage.FinancialItem{{Currency: "USD", IncludedRemainingMinor: &amount,
					Scope: &usage.UsageScope{Kind: "team", SharedPool: &shared}}},
			},
		},
	}
	// When
	copied := copyUsageData(source)
	source.Buckets[0].Measurement = usage.MeasurementPercent
	*source.UsageDetails.Finances.ObservedAt = now.Add(time.Hour)
	source.UsageDetails.Finances.Items[0].Currency = "EUR"
	*source.UsageDetails.Finances.Items[0].IncludedRemainingMinor = 0
	*source.UsageDetails.Finances.Items[0].Scope.SharedPool = false
	// Then
	if copied.Buckets[0].Measurement != usage.MeasurementStatus || copied.UsageDetails == nil || copied.UsageDetails.Finances == nil {
		t.Fatal("copy lost measurement or details")
	}
	finances := copied.UsageDetails.Finances
	if finances.ObservedAt.Hour() != 12 || len(finances.Items) != 1 {
		t.Fatal("copied observation or items changed with source")
	}
	item := finances.Items[0]
	if item.Currency != "USD" || *item.IncludedRemainingMinor != 500 || !*item.Scope.SharedPool {
		t.Fatal("copied financial row changed with source")
	}
}

func TestPollerDetailsRemainIndependentAcrossPublicationAndReads(t *testing.T) {
	// Given
	label := "Pro"
	details := &usage.UsageDetails{Plan: &usage.PlanDetails{Label: &label}}
	provider := &sequenceProvider{name: "Example", responses: []providerResult{{
		data: usage.UsageData{UsageDetails: details},
	}}}
	p := New(Options{Clock: fixedClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	// When
	published := p.PollAll(context.Background())
	*details.Plan.Label = "provider mutation"
	*published[0].Data.UsageDetails.Plan.Label = "caller mutation"
	snapshot := p.Snapshot()
	entry, ok := p.Get("Example")
	// Then
	if !ok || len(snapshot) != 1 || entry.Data.UsageDetails == nil || snapshot[0].Data.UsageDetails == nil {
		t.Fatal("cached details missing")
	}
	if *entry.Data.UsageDetails.Plan.Label != "Pro" || *snapshot[0].Data.UsageDetails.Plan.Label != "Pro" {
		t.Fatal("cached details changed through provider or published result")
	}
	if entry.Data.UsageDetails.Plan.Label == snapshot[0].Data.UsageDetails.Plan.Label ||
		entry.Data.UsageDetails.Plan.Label == p.providers["example"].entry.Data.UsageDetails.Plan.Label ||
		snapshot[0].Data.UsageDetails.Plan.Label == p.providers["example"].entry.Data.UsageDetails.Plan.Label {
		t.Fatal("Get or Snapshot aliases stored details")
	}
}

func TestPollerErrorOverlaySuppressesLastGoodDetailsInJSON(t *testing.T) {
	// Given
	label, failure := "Pro", "unavailable"
	provider := &sequenceProvider{name: "Example", responses: []providerResult{
		{data: usage.UsageData{UsageDetails: &usage.UsageDetails{Plan: &usage.PlanDetails{Label: &label}}}},
		{data: usage.UsageData{Error: &failure}},
	}}
	p := New(Options{Clock: fixedClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	p.PollAll(context.Background())
	// When
	entry := p.PollAll(context.Background())[0]
	encoded, err := json.Marshal(entry.Data)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["usage_details"]) != "null" || string(fields["is_success"]) != "false" {
		t.Fatal("error overlay published last-good details as current")
	}
}
