package app

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_buildPoller_registers_all_default_providers(t *testing.T) {
	// Given
	cfg := config.Defaults()

	// When
	_, _, _, _, _, names, err := buildPoller(cfg, discardLogger())

	// Then
	if err != nil {
		t.Fatalf("buildPoller: %v", err)
	}
	sort.Strings(names)
	want := []string{"Claude", "Codex", "Cursor", "Grok", "OpenCodeGo"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func Test_buildPoller_registers_claude_accounts(t *testing.T) {
	// Given
	workDir := filepath.Join(t.TempDir(), "work")
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"claude": {
			Enabled: true,
			Accounts: []config.AccountConfig{
				{ID: "work", Label: "Work", ConfigDir: workDir, Enabled: true},
				{ID: "off", Label: "Off", ConfigDir: filepath.Join(t.TempDir(), "off"), Enabled: false},
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
	workDir := filepath.Join(t.TempDir(), "work")
	cfg := config.Defaults()
	cfg.Providers = map[string]config.ProviderConfig{
		"claude": {
			Enabled: false,
			Accounts: []config.AccountConfig{
				{ID: "work", Label: "Work", ConfigDir: workDir, Enabled: true},
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
