package accountconfig_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
	"gopkg.in/yaml.v3"
)

func targetConfig(t *testing.T, body string) accountconfig.Target {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return accountconfig.Target{Path: path, Env: []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config")}}
}

func activeAccount(id string) accountconfig.Account {
	return accountconfig.Account{ID: id, ConfigDir: "~/.claude-" + id, Enabled: true}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func Test_Add_creates_missing_file_and_private_parent(t *testing.T) {
	target := targetConfig(t, "")
	target.Path = filepath.Join(filepath.Dir(target.Path), "new", "config.yaml")
	change, err := accountconfig.Add(context.Background(), target, activeAccount("work"))
	if err != nil {
		t.Fatal(err)
	}
	if change.Before != nil || change.Path != target.Path || !bytes.Equal(change.After, fileBytes(t, target.Path)) {
		t.Fatalf("change=%+v", change)
	}
	for _, tt := range []struct {
		path string
		mode os.FileMode
	}{{target.Path, 0o600}, {filepath.Dir(target.Path), 0o700}} {
		info, err := os.Stat(tt.path)
		if err != nil {
			t.Fatalf("stat %s: %v", tt.path, err)
		}
		if !modeMatches(info.Mode(), tt.mode) {
			t.Fatalf("mode of %s = %v, want %v", tt.path, info.Mode().Perm(), tt.mode)
		}
	}
	accounts, err := accountconfig.List(context.Background(), target)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "work" || !accounts[0].Enabled {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
}

func Test_Add_handles_empty_account_layouts(t *testing.T) {
	for _, body := range []string{"", "# empty configuration\n", "other: keep\n", "providers: {}\n", "providers:\n  claude: {}\n", "providers:\n  CLAUDE:\n    accounts:\n", "providers:\n  Claude:\n    accounts: ~\n", "providers:\n  claude:\n    accounts: []\n"} {
		t.Run(body, func(t *testing.T) {
			target := targetConfig(t, body)
			change, err := accountconfig.Add(context.Background(), target, activeAccount("work"))
			if err != nil {
				t.Fatal(err)
			}
			if change.Account.Label != "work" || change.Account.ConfigDir != os.Getenv("HOME")+"/.claude-work" {
				t.Fatalf("account=%+v", change.Account)
			}
			if strings.Contains(body, "CLAUDE") && !bytes.Contains(change.After, []byte("CLAUDE:")) {
				t.Fatal("provider spelling changed")
			}
			if strings.HasPrefix(body, "#") && !bytes.HasPrefix(change.After, []byte(body)) {
				t.Fatalf("comment-only config text lost:\n%s", change.After)
			}
		})
	}
}

func Test_Add_preserves_comments_unknown_values_quotes_indent_and_line_endings(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		t.Run(map[bool]string{false: "LF", true: "CRLF BOM"}[crlf], func(t *testing.T) {
			body := "# private settings\nunknown:\n    flag: true\n    number: 17\nproviders:\n    Claude:\n        # retain me\n        custom: 'quoted'\n        accounts: []\n"
			if crlf {
				body = "\ufeff" + strings.ReplaceAll(body, "\n", "\r\n")
			}
			target := targetConfig(t, body)
			account := activeAccount("work")
			account.Label = "true"
			account.ConfigDir = "~/work/../cafe\u0301 /"
			change, err := accountconfig.Add(context.Background(), target, account)
			if err != nil {
				t.Fatal(err)
			}
			for _, keep := range []string{"# private settings", "# retain me", "custom: 'quoted'", "    Claude:"} {
				if !bytes.Contains(change.After, []byte(keep)) {
					t.Fatalf("lost %q", keep)
				}
			}
			if crlf && (!bytes.HasPrefix(change.After, []byte{0xef, 0xbb, 0xbf}) || bytes.Contains(bytes.ReplaceAll(change.After, []byte("\r\n"), nil), []byte("\n"))) {
				t.Fatal("BOM or CRLF lost")
			}
			var got map[string]any
			if err := yaml.Unmarshal(change.After, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["unknown"], map[string]any{"flag": true, "number": 17}) {
				t.Fatalf("unknown=%+v", got["unknown"])
			}
			if change.Account.ConfigDir != os.Getenv("HOME")+"/work/../cafe\u0301 /" || change.Account.Label != "true" {
				t.Fatalf("account spelling=%+v", change.Account)
			}
		})
	}
}

func Test_Remove_preserves_order_and_other_settings(t *testing.T) {
	for _, id := range []string{"middle", "last", "only"} {
		t.Run(id, func(t *testing.T) {
			items := []string{"first", "middle", "last"}
			if id == "only" {
				items = []string{"only"}
			}
			body := "unknown: keep\nproviders:\n  claude:\n    accounts:\n"
			var want []string
			for _, item := range items {
				body += "      - id: " + item + "\n        config_dir: ~/.claude-" + item + "\n"
				if item != id {
					want = append(want, item)
				}
			}
			target := targetConfig(t, body)
			change, err := accountconfig.Remove(context.Background(), target, id)
			if err != nil {
				t.Fatal(err)
			}
			accounts, err := accountconfig.List(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, a := range accounts {
				got = append(got, a.ID)
			}
			if !reflect.DeepEqual(got, want) || change.Account.ID != id || !bytes.Contains(change.After, []byte("unknown: keep")) {
				t.Fatalf("got=%v want=%v change=%+v", got, want, change.Account)
			}
		})
	}
}

func Test_Add_writes_through_config_symlink(t *testing.T) {
	target := targetConfig(t, "unknown: keep\n")
	real := target.Path
	target.Path = filepath.Join(t.TempDir(), "linked.yaml")
	if err := os.Symlink(real, target.Path); err != nil {
		t.Fatal(err)
	}
	_, err := accountconfig.Add(context.Background(), target, activeAccount("work"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fileBytes(t, real), []byte("id: work")) {
		t.Fatal("target not updated")
	}
	info, err := os.Lstat(target.Path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link lost: %v %v", info, err)
	}
	info, err = os.Stat(real)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if !modeMatches(info.Mode(), 0o640) {
		t.Fatalf("mode changed: %v", info.Mode().Perm())
	}
}
