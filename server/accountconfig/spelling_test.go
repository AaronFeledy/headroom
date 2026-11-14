package accountconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func Test_ExpandConfigDir_preserves_exact_spelling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tt := range []struct{ input, want string }{
		{"~", home}, {"~/x/../cafe\u0301 /", home + "/x/../cafe\u0301 /"}, {`~\x\`, home + `\x\`}, {home + "/x//", home + "/x//"},
	} {
		got, err := accountconfig.ExpandConfigDir(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("ExpandConfigDir(%q)=%q,%v want=%q", tt.input, got, err, tt.want)
		}
	}
	if _, err := accountconfig.ExpandConfigDir("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	got, err := accountconfig.DefaultClaudeConfigDir("work")
	if err != nil || got != home+string(os.PathSeparator)+".claude-work" {
		t.Fatalf("default=%q err=%v", got, err)
	}
}

func Test_Add_uses_current_bytes_for_concurrent_additions(t *testing.T) {
	target := targetConfig(t, "other: keep\n")
	var group sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 2)
	for _, id := range []string{"work", "side"} {
		group.Go(func() {
			<-start
			_, err := accountconfig.Add(context.Background(), target, activeAccount(id))
			errors <- err
		})
	}
	close(start)
	group.Wait()
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := accountconfig.List(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID == accounts[1].ID {
		t.Fatalf("lost concurrent edit: %+v", accounts)
	}
}

func Test_List_honors_target_path_and_enabled_accounts(t *testing.T) {
	target := targetConfig(t, "providers:\n  claude:\n    accounts:\n      - id: off\n        config_dir: ~/.claude-off\n        enabled: false\n")
	target.Args = []string{"--config", filepath.Join(t.TempDir(), "other.yaml")}
	accounts, err := accountconfig.List(context.Background(), target)
	if err != nil || len(accounts) != 1 || accounts[0].Enabled || accounts[0].ID != "off" {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
}
