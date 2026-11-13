package poller

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Poller_ManualQA_five_providers_start_before_release_and_transient_error_retains_usage(t *testing.T) {
	if testing.Short() {
		t.Skip("manual QA evidence is skipped in short mode")
	}
	// Given
	started := make(chan string, 5)
	release := make(chan struct{})
	poller := New(Options{Clock: fixedClock{now: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}})
	for _, name := range []string{"Grok", "Codex", "Cursor", "Claude", "OpenCodeGo"} {
		if err := poller.Register(&barrierProvider{name: name, started: started, release: release, utilization: float64(len(name))}, true); err != nil {
			t.Fatalf("Register %s returned error: %v", name, err)
		}
	}
	done := make(chan []Entry, 1)

	// When
	go func() { done <- poller.PollAll(context.Background()) }()
	beginOrder := []string{<-started, <-started, <-started, <-started, <-started}
	close(release)
	entries := <-done
	transient := &sequenceProvider{name: "Transient", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Transient", Current: usage.UsageBucket{Utilization: 88}}},
		{err: errors.New("synthetic upstream unavailable")},
	}}
	poller2 := New(Options{Clock: fixedClock{now: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}})
	if err := poller2.Register(transient, true); err != nil {
		t.Fatalf("Register transient returned error: %v", err)
	}
	poller2.PollAll(context.Background())
	retained := poller2.PollAll(context.Background())[0]

	// Then
	sortedBegin := append([]string(nil), beginOrder...)
	sort.Strings(sortedBegin)
	t.Logf("manual QA synthetic begin_order=%v sorted_results=%v transient_retained_utilization=%.0f transient_error=%q", sortedBegin, entryNames(entries), retained.Data.Current.Utilization, deref(retained.Data.Error))
	if got := entryNames(entries); !reflect.DeepEqual(got, []string{"Claude", "Codex", "Cursor", "Grok", "OpenCodeGo"}) {
		t.Fatalf("sorted results = %v", got)
	}
	if retained.Data.Current.Utilization != 88 || retained.Data.Error == nil {
		t.Fatalf("transient retained entry = %+v", retained)
	}
}
