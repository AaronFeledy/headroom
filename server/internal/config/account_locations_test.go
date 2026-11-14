package config_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Load_config_dir_preserves_literal_spaces_for_keychain_hashing(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := writeAccountConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        config_dir: \"~/work /\"\n")
	// When
	cfg, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["claude"].Accounts[0].ConfigDir != home+"/work /" {
		t.Fatal("config dir spelling changed")
	}
}

func Test_Load_explicit_default_file_can_share_directory_with_distinct_account_file(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, "shared")
	path := writeAccountConfig(t, "providers:\n  claude:\n    credentials_path: "+filepath.Join(dir, "custom.json")+"\n    accounts:\n      - id: work\n        config_dir: "+dir+"\n")
	// When
	_, err := config.LoadForOS(context.Background(), config.LoadOptions{Args: []string{"-config", path}}, "linux")
	// Then
	if err != nil {
		t.Fatalf("distinct credential files rejected: %v", err)
	}
}
