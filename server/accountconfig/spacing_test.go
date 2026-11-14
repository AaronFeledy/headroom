package accountconfig_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func Test_Account_edits_preserve_blank_lines_and_literal_scalars(t *testing.T) {
	for _, operation := range []string{"add", "remove", "update"} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(operation+newline, func(t *testing.T) {
				body := "listen_addr: 127.0.0.1:7823\n\n\nunknown:\n  key: |\n    first\n\n    second\n\nproviders:\n  claude:\n    enabled: false\n\n    accounts:\n      - id: work\n        config_dir: ~/.claude-work\n      - id: spare\n        config_dir: ~/.claude-spare\n\n  codex:\n    enabled: false\n\nlast: keep\n"
				target := targetConfig(t, strings.ReplaceAll(body, "\n", newline))
				var change accountconfig.Change
				var err error
				switch operation {
				case "add":
					change, err = accountconfig.Add(context.Background(), target, activeAccount("side"))
				case "remove":
					change, err = accountconfig.Remove(context.Background(), target, "work")
				case "update":
					change, err = accountconfig.Update(context.Background(), target, "work", accountconfig.AccountChanges{Label: pointer("Team")})
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, fragment := range []string{"7823\n\n\nunknown:", "key: |\n    first\n\n    second\n", "\n\nproviders:", "enabled: false\n\n    accounts:", "\n\n  codex:", "\n\nlast: keep"} {
					if !bytes.Contains(change.After, []byte(strings.ReplaceAll(fragment, "\n", newline))) {
						t.Fatalf("lost spacing %q:\n%s", fragment, change.After)
					}
				}
				if newline == "\r\n" && bytes.Contains(bytes.ReplaceAll(change.After, []byte("\r\n"), nil), []byte("\n")) {
					t.Fatal("mixed line endings")
				}
			})
		}
	}
}
