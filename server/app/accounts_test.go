package app

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_buildPoller_registers_all_default_providers(t *testing.T) {
	// Given
	cfg := config.Defaults()

	// When
	_, _, _, _, _, names, err := buildPoller(cfg, discardLogger(), nil)

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

func Test_buildPoller_account_sign_in_command_uses_supplied_environment(t *testing.T) {
	for _, root := range []string{"", "/managed/install"} {
		t.Run(root, func(t *testing.T) {
			t.Setenv("HEADROOM_INSTALL_ROOT", "/must-not-read-process-environment")
			dir := filepath.Join(t.TempDir(), "work")
			cfg := config.Defaults()
			cfg.Providers = map[string]config.ProviderConfig{"claude": {
				Enabled: true, CredentialsPath: filepath.Join(t.TempDir(), "missing.json"),
				Accounts: []config.AccountConfig{{ID: "work", Label: "Work", ConfigDir: dir, Enabled: true}},
			}}
			p, _, _, _, _, _, err := buildPoller(cfg, discardLogger(), []string{"HEADROOM_INSTALL_ROOT=" + root})
			if err != nil {
				t.Fatal(err)
			}
			entries := p.PollAll(context.Background())
			if len(entries) != 2 {
				t.Fatalf("entries=%d", len(entries))
			}
			for _, entry := range entries {
				command := entry.Data.Auth.SignInCommand
				if command == nil {
					t.Fatal("missing sign-in command")
				}
				if entry.Data.Account == nil {
					if *command != "claude auth login" {
						t.Fatalf("default command=%q", *command)
					}
				} else if root != "" {
					if *command != "headroom accounts login claude work" {
						t.Fatalf("managed command=%q", *command)
					}
				} else {
					prefix := "CLAUDE_CONFIG_DIR="
					if runtime.GOOS == "windows" {
						prefix = "$previous = $env:CLAUDE_CONFIG_DIR;"
					}
					if !strings.HasPrefix(*command, prefix) || !strings.Contains(*command, dir) {
						t.Fatalf("unmanaged command=%q", *command)
					}
				}
			}
		})
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
	_, _, claudeClient, _, _, names, err := buildPoller(cfg, discardLogger(), nil)
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
	_, _, claudeClient, _, _, names, err := buildPoller(cfg, discardLogger(), nil)
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
