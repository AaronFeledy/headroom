package accountconfig_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func Test_Add_refuses_duplicates_and_limit_without_changing_file(t *testing.T) {
	for _, kind := range []string{"id", "label", "symlink dir", "ninth", "environment collision"} {
		t.Run(kind, func(t *testing.T) {
			target := targetConfig(t, "providers:\n  claude:\n    enabled: false\n    accounts:\n      - id: work\n        label: Work\n        config_dir: ~/.claude-work\n")
			account := activeAccount("side")
			var want error
			switch kind {
			case "id":
				account.ID = "work"
				want = accountconfig.ErrDuplicateID
			case "label":
				account.Label = "wOrK"
				want = accountconfig.ErrDuplicateLabel
			case "symlink dir":
				dir := filepath.Join(os.Getenv("HOME"), ".claude-work")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(os.Getenv("HOME"), "linked")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				account.ConfigDir = link
				want = accountconfig.ErrDuplicateConfigDir
			case "ninth":
				body := "providers:\n  claude:\n    accounts:\n"
				for i := 0; i < 8; i++ {
					body += fmt.Sprintf("      - id: a%d\n        config_dir: ~/.claude-a%d\n", i, i)
				}
				if err := os.WriteFile(target.Path, []byte(body), 0o640); err != nil {
					t.Fatal(err)
				}
				want = accountconfig.ErrTooManyAccounts
			case "environment collision":
				account.ConfigDir = "~/.claude"
				target.Env = append(target.Env, "USAGE_PROVIDER_CLAUDE_ENABLED=true")
				want = accountconfig.ErrDuplicateConfigDir
			}
			before := fileBytes(t, target.Path)
			_, err := accountconfig.Add(context.Background(), target, account)
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			if !bytes.Equal(fileBytes(t, target.Path), before) {
				t.Fatal("rejected add changed file")
			}
		})
	}
}

func Test_Account_edits_refuse_unsupported_layouts(t *testing.T) {
	for _, body := range []string{
		"providers: &p\n  claude: {}\n",
		"base: &b {}\nproviders:\n  <<: *b\n  claude: {}\n",
		"base: &b {}\nproviders:\n  claude: *b\n",
		"providers:\n  claude:\n    accounts: &a []\n",
		"providers: []\n", "providers:\n  claude: null\n", "providers:\n  claude:\n    accounts: {}\n",
		"providers:\n  claude: {}\n  CLAUDE: {}\n",
		"providers: {}\n---\nother: true\n",
	} {
		t.Run(body, func(t *testing.T) {
			target := targetConfig(t, body)
			_, err := accountconfig.Add(context.Background(), target, activeAccount("work"))
			if !errors.Is(err, accountconfig.ErrUnsupportedLayout) {
				t.Fatalf("err=%v", err)
			}
			if !bytes.Equal(fileBytes(t, target.Path), []byte(body)) {
				t.Fatal("unsupported layout was rewritten")
			}
		})
	}
}

func Test_CheckAdd_and_List_redact_invalid_config_values(t *testing.T) {
	for _, body := range []string{"poll_interval: SUPER_SECRET\n", "ssh_access: SUPER_SECRET\n", "providers:\n  claude:\n    accounts:\n      - id: SUPER_SECRET\n        config_dir: /tmp/fixture\n", "providers: [SUPER_SECRET\n", "providers:\n  SUPER_SECRET:\n    accounts: []\n"} {
		t.Run(body, func(t *testing.T) {
			target := targetConfig(t, body)
			for _, operation := range []func() error{
				func() error { return accountconfig.CheckAdd(context.Background(), target, activeAccount("work")) },
				func() error { _, err := accountconfig.List(context.Background(), target); return err },
				func() error {
					_, err := accountconfig.Update(context.Background(), target, "work", accountconfig.AccountChanges{Enabled: pointer(false)})
					return err
				},
			} {
				err := operation()
				var validation *accountconfig.ValidationError
				if !errors.As(err, &validation) || strings.Contains(strings.ToLower(err.Error()), "super_secret") {
					t.Fatalf("unsafe error: %v", err)
				}
			}
			if !bytes.Equal(fileBytes(t, target.Path), []byte(body)) {
				t.Fatal("validation wrote file")
			}
		})
	}
}

func Test_Remove_unknown_account_and_Restore_CAS(t *testing.T) {
	target := targetConfig(t, "# original\nother: true\n")
	_, err := accountconfig.Remove(context.Background(), target, "missing")
	if !errors.Is(err, accountconfig.ErrAccountNotFound) {
		t.Fatalf("err=%v", err)
	}
	change, err := accountconfig.Add(context.Background(), target, activeAccount("work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := accountconfig.Restore(context.Background(), target, change.Before, change.After); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes(t, target.Path), change.Before) {
		t.Fatal("rollback did not restore exact bytes")
	}
	err = accountconfig.Restore(context.Background(), target, change.Before, change.After)
	if !errors.Is(err, accountconfig.ErrConfigChanged) {
		t.Fatalf("CAS err=%v", err)
	}
}

func Test_CheckAdd_does_not_create_missing_file(t *testing.T) {
	target := targetConfig(t, "")
	target.Path = filepath.Join(filepath.Dir(target.Path), "missing", "config.yaml")
	if err := accountconfig.CheckAdd(context.Background(), target, activeAccount("work")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(target.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check created directory: %v", err)
	}
}

func Test_CheckAdd_names_the_rule_the_requested_account_breaks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account accountconfig.Account
		want    error
	}{
		{"uppercase id", accountconfig.Account{ID: "WORK", ConfigDir: "~/.claude-work", Enabled: true}, accountconfig.ErrInvalidID},
		{"long label", accountconfig.Account{ID: "work", Label: "a label that is far longer than thirty-two", ConfigDir: "~/.claude-work", Enabled: true}, accountconfig.ErrInvalidLabel},
		{"relative dir", accountconfig.Account{ID: "work", ConfigDir: "claude-work", Enabled: true}, accountconfig.ErrInvalidConfigDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := targetConfig(t, "")
			if err := accountconfig.CheckAdd(context.Background(), target, tc.account); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func Test_Add_allows_default_directory_when_default_account_is_disabled(t *testing.T) {
	target := targetConfig(t, "providers:\n  claude:\n    enabled: false\n")
	account := activeAccount("main")
	account.ConfigDir = "~/.claude"

	if _, err := accountconfig.Add(context.Background(), target, account); err != nil {
		t.Fatal(err)
	}
}
