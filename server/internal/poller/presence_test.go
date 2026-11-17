package poller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/presence"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Poller_stamps_account_presence_from_evidence_and_memory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	store := presence.New(presence.Options{Dir: dir})
	provider := &sequenceProvider{name: "Grok", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_out", nil)}},
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_in", nil)}},
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_out", nil)}},
	}}
	poller := New(Options{Clock: fixedClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}, Presence: store})
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	first, _, err := poller.PollProvider(context.Background(), "Grok")
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if first.Data.AccountPresence != usage.PresenceAbsent {
		t.Fatalf("first presence = %q, want absent", first.Data.AccountPresence)
	}
	if store.Present("Grok") {
		t.Fatal("absent reading was remembered")
	}

	second, _, err := poller.PollProvider(context.Background(), "Grok")
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if second.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("second presence = %q, want present", second.Data.AccountPresence)
	}
	if !store.Present("Grok") {
		t.Fatal("signed-in reading was not remembered")
	}

	third, _, err := poller.PollProvider(context.Background(), "Grok")
	if err != nil {
		t.Fatalf("third poll: %v", err)
	}
	if third.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("presence after sign-out = %q, want present from memory", third.Data.AccountPresence)
	}
	cached, ok := poller.Get("Grok")
	if !ok || cached.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("cached presence = %q ok=%v, want present", cached.Data.AccountPresence, ok)
	}
}

func Test_Poller_account_presence_is_evidence_only_without_a_store(t *testing.T) {
	provider := &sequenceProvider{name: "Grok", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_in", nil)}},
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_out", nil)}},
	}}
	poller := New(Options{Clock: fixedClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}})
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	first, _, _ := poller.PollProvider(context.Background(), "Grok")
	if first.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("signed-in presence = %q, want present", first.Data.AccountPresence)
	}
	second, _, _ := poller.PollProvider(context.Background(), "Grok")
	if second.Data.AccountPresence != usage.PresenceAbsent {
		t.Fatalf("signed-out presence = %q, want absent without memory", second.Data.AccountPresence)
	}
}

func Test_Poller_uses_presence_memory_from_a_previous_run(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provider-presence")
	presence.New(presence.Options{Dir: dir}).Record("Grok")
	provider := &sequenceProvider{name: "Grok", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Grok", Auth: usage.NewAuth("Grok", "signed_out", nil)}},
	}}
	poller := New(Options{
		Clock:    fixedClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)},
		Presence: presence.New(presence.Options{Dir: dir}),
	})
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	entry, _, _ := poller.PollProvider(context.Background(), "Grok")

	if entry.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("presence = %q, want present from the remembered row", entry.Data.AccountPresence)
	}
}

func Test_Poller_marks_extra_account_rows_present_without_evidence(t *testing.T) {
	provider := &accountProvider{
		name:         "Claude (Work)",
		providerType: "Claude",
		account:      &usage.Account{ID: "work", Label: "Work"},
		responses:    []providerResult{{data: usage.UsageData{ProviderName: "Claude (Work)", Auth: usage.NewAuth("Claude", "signed_out", nil)}}},
	}
	poller := New(Options{Clock: fixedClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}})
	if err := poller.Register(provider, true); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	entry, _, _ := poller.PollProvider(context.Background(), "Claude (Work)")

	if entry.Data.AccountPresence != usage.PresencePresent {
		t.Fatalf("presence = %q, want present for a configured account", entry.Data.AccountPresence)
	}
	if entry.Data.RowKey() != "Claude:work" {
		t.Fatalf("row key = %q, want Claude:work", entry.Data.RowKey())
	}
}
