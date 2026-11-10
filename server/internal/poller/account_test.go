package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type accountProvider struct {
	name         string
	providerType string
	account      *usage.Account
	responses    []providerResult
	calls        int
}

func (p *accountProvider) Name() string { return p.name }

func (p *accountProvider) ProviderType() string { return p.providerType }

func (p *accountProvider) Account() *usage.Account { return p.account.Clone() }

func (p *accountProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	if err := ctx.Err(); err != nil {
		return usage.UsageData{ProviderName: p.name}, err
	}
	if p.calls >= len(p.responses) {
		p.calls = len(p.responses) - 1
	}
	result := p.responses[p.calls]
	p.calls++
	if result.panicValue != nil {
		panic(result.panicValue)
	}
	if result.data.ProviderName == "" {
		result.data.ProviderName = p.name
	}
	return result.data, result.err
}

func Test_Poller_stamps_account_identity_on_success_error_panic_and_overlay(t *testing.T) {
	// Given
	account := &usage.Account{ID: "work", Label: "Work"}
	provider := &accountProvider{
		name:         "Claude (Work)",
		providerType: "Claude",
		account:      account,
		responses: []providerResult{
			{data: usage.UsageData{ProviderName: "Claude (Work)", ProviderType: "Wrong", Account: &usage.Account{ID: "wrong", Label: "Wrong"}, Current: usage.UsageBucket{Utilization: 10}}},
			{data: usage.UsageData{ProviderName: "Claude (Work)", Error: strPtr("AUTH_EXPIRED")}},
			{panicValue: "boom"},
			{err: errors.New("network")},
		},
	}
	poller := New(Options{Clock: &stepClock{values: []time.Time{
		time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 10, 3, 0, 0, time.UTC),
	}}})
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// When / Then success
	success := poller.PollAll(context.Background())[0].Data
	assertStampedAccount(t, success, "Claude")
	if success.Error != nil {
		t.Fatalf("success error = %v", success.Error)
	}

	// expected error overlay keeps last-good meters
	errored := poller.PollAll(context.Background())[0].Data
	assertStampedAccount(t, errored, "Claude")
	if errored.Current.Utilization != 10 || errored.Error == nil {
		t.Fatalf("error overlay = %+v", errored)
	}

	// panic overlay
	panicked := poller.PollAll(context.Background())[0].Data
	assertStampedAccount(t, panicked, "Claude")
	encoded, err := json.Marshal(panicked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"sign_in_command":null`) {
		t.Fatalf("panic account row must null sign_in_command, got %s", encoded)
	}

	// failure overlay after last-good
	failed := poller.PollAll(context.Background())[0].Data
	assertStampedAccount(t, failed, "Claude")
	if failed.Current.Utilization != 10 || failed.Error == nil {
		t.Fatalf("failure overlay = %+v", failed)
	}
}

func Test_Poller_plain_provider_gets_type_equal_name_and_nil_account(t *testing.T) {
	// Given
	provider := &sequenceProvider{name: "Codex", responses: []providerResult{{data: usage.UsageData{}}}}
	poller := New(Options{})
	if err := poller.Register(provider, true); err != nil {
		t.Fatal(err)
	}

	// When
	data := poller.PollAll(context.Background())[0].Data

	// Then
	if data.ProviderType != "Codex" || data.Account != nil {
		t.Fatalf("type=%q account=%v", data.ProviderType, data.Account)
	}
}

func Test_Poller_sortEntries_places_account_rows_after_defaults(t *testing.T) {
	// Given
	poller := New(Options{})
	for _, p := range []usage.Provider{
		&sequenceProvider{name: "Grok", responses: []providerResult{{}}},
		&sequenceProvider{name: "Claude", responses: []providerResult{{}}},
		&accountProvider{name: "Claude (Work)", providerType: "Claude", account: &usage.Account{ID: "work", Label: "Work"}, responses: []providerResult{{}}},
		&sequenceProvider{name: "Codex", responses: []providerResult{{}}},
		&sequenceProvider{name: "Cursor", responses: []providerResult{{}}},
	} {
		if err := poller.Register(p, true); err != nil {
			t.Fatal(err)
		}
	}

	// When
	poller.PollAll(context.Background())
	snapshot := poller.Snapshot()
	rows := make([]usage.UsageData, len(snapshot))
	for i, entry := range snapshot {
		rows[i] = entry.Data
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		ProviderName string `json:"provider_name"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(decoded))
	for i, row := range decoded {
		names[i] = row.ProviderName
	}

	// Then
	want := []string{"Claude", "Codex", "Cursor", "Grok", "Claude (Work)"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

func Test_Diagnostics_logs_provider_type_and_account_id_for_account_row(t *testing.T) {
	// Given
	provider := &accountProvider{
		name:         "Claude (Work)",
		providerType: "Claude",
		account:      &usage.Account{ID: "work", Label: "Work"},
		responses:    []providerResult{{data: usage.UsageData{}}},
	}
	p, _, _ := recoveryPoller(t, provider)
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	if _, _, err := p.PollProvider(context.Background(), "Claude (Work)"); err != nil {
		t.Fatal(err)
	}

	// Then
	line := logs.String()
	if !strings.Contains(line, `"provider":"Claude"`) || !strings.Contains(line, `"account":"work"`) {
		t.Fatalf("diagnostics log missing type/account: %s", line)
	}
	if strings.Contains(line, "Work") {
		t.Fatalf("must not log account label: %s", line)
	}
}

func assertStampedAccount(t *testing.T, data usage.UsageData, wantType string) {
	t.Helper()
	if data.ProviderType != wantType {
		t.Fatalf("ProviderType = %q, want %q", data.ProviderType, wantType)
	}
	if data.Account == nil || data.Account.ID != "work" || data.Account.Label != "Work" {
		t.Fatalf("Account = %+v", data.Account)
	}
}
