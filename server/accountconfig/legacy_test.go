package accountconfig_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func Test_Legacy_credentials_path_account_lists_changes_removes_and_adds(t *testing.T) {
	// Given
	target := targetConfig(t, "providers:\n  claude:\n    accounts:\n      - id: work\n        label: Work\n        credentials_path: ~/.claude-work/.credentials.json\n")
	wantDir := filepath.Join(os.Getenv("HOME"), ".claude-work")

	// When
	accounts, err := accountconfig.List(context.Background(), target)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ConfigDir != wantDir || accounts[0].Label != "Work" {
		t.Fatalf("accounts = %+v", accounts)
	}

	// When changing a legacy entry, it stays valid and keeps its directory.
	label := "Work Team"
	change, err := accountconfig.Update(context.Background(), target, "work", accountconfig.AccountChanges{Label: &label})
	if err != nil {
		t.Fatal(err)
	}
	if change.Account.Label != label || change.Account.ConfigDir != wantDir {
		t.Fatalf("changed account = %+v", change.Account)
	}
	accounts, err = accountconfig.List(context.Background(), target)
	if err != nil || len(accounts) != 1 || accounts[0].Label != label || accounts[0].ConfigDir != wantDir {
		t.Fatalf("accounts after change = %+v, err = %v", accounts, err)
	}

	// When removing a legacy entry, only the entry disappears.
	if _, err := accountconfig.Remove(context.Background(), target, "work"); err != nil {
		t.Fatal(err)
	}
	accounts, err = accountconfig.List(context.Background(), target)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts after remove = %+v, err = %v", accounts, err)
	}

	// When adding an account, the config is written in the current format.
	if _, err := accountconfig.Add(context.Background(), target, activeAccount("side")); err != nil {
		t.Fatal(err)
	}
	data := fileBytes(t, target.Path)
	if !bytes.Contains(data, []byte("config_dir: "+filepath.Join(os.Getenv("HOME"), ".claude-side"))) {
		t.Fatalf("added account lacks config_dir:\n%s", data)
	}
	if bytes.Contains(data, []byte("credentials_path:")) {
		t.Fatalf("added account kept the legacy spelling:\n%q", data)
	}
}
