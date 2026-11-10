package app

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_buildPoller_registers_claude_accounts(t *testing.T) {
	// Given
	workPath := filepath.Join(t.TempDir(), "work.json")
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"claude": {
			Enabled: true,
			Accounts: []config.AccountConfig{
				{ID: "work", Label: "Work", CredentialsPath: workPath, Enabled: true},
				{ID: "off", Label: "Off", CredentialsPath: filepath.Join(t.TempDir(), "off.json"), Enabled: false},
			},
		},
	}

	// When
	_, _, claudeClient, _, _, names, err := buildPoller(cfg, discardLogger())
	if err != nil {
		t.Fatalf("buildPoller: %v", err)
	}

	// Then
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"Claude", "Claude (Work)"}) {
		t.Fatalf("names = %v", names)
	}
	if claudeClient == nil || claudeClient.Name() != "Claude" {
		t.Fatalf("default claude client = %v", claudeClient)
	}
}

func Test_buildPoller_extra_account_when_default_disabled(t *testing.T) {
	// Given
	workPath := filepath.Join(t.TempDir(), "work.json")
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"claude": {
			Enabled: false,
			Accounts: []config.AccountConfig{
				{ID: "work", Label: "Work", CredentialsPath: workPath, Enabled: true},
			},
		},
		"codex":  {Enabled: false},
		"cursor": {Enabled: false},
		"grok":   {Enabled: false},
	}

	// When
	_, _, claudeClient, _, _, names, err := buildPoller(cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if !reflect.DeepEqual(names, []string{"Claude (Work)"}) {
		t.Fatalf("names = %v", names)
	}
	if claudeClient != nil {
		t.Fatalf("default claude client should be nil, got %v", claudeClient.Name())
	}
}
