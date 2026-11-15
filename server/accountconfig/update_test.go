package accountconfig_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func pointer[T any](value T) *T { return &value }

func Test_Update_changes_only_requested_fields(t *testing.T) {
	for _, tc := range []struct {
		name, fields, label string
		enabled             bool
		changes             accountconfig.AccountChanges
	}{
		{"append label", "", "Team", true, accountconfig.AccountChanges{Label: pointer(" Team ")}},
		{"replace label", "        label: 'Old' # keep\n", "Team", true, accountconfig.AccountChanges{Label: pointer("Team")}},
		{"disable", "", "work", false, accountconfig.AccountChanges{Enabled: pointer(false)}},
		{"enable", "        enabled: false\n", "work", true, accountconfig.AccountChanges{Enabled: pointer(true)}},
		{"both", "        enabled: false\n", "Team", true, accountconfig.AccountChanges{Label: pointer("Team"), Enabled: pointer(true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "unknown: keep\nproviders:\n  claude:\n    accounts:\n      - id: work\n        config_dir: ~/.claude-work/\n        custom: 'untouched'\n" + tc.fields
			target := targetConfig(t, body)
			change, err := accountconfig.Update(context.Background(), target, "work", tc.changes)
			if err != nil {
				t.Fatal(err)
			}
			if change.Account.ID != "work" || change.Account.Label != tc.label || change.Account.Enabled != tc.enabled || !strings.HasSuffix(change.Account.ConfigDir, "/.claude-work/") {
				t.Fatalf("effective account=%+v", change.Account)
			}
			if !bytes.Equal(change.Before, []byte(body)) || !bytes.Equal(change.After, fileBytes(t, target.Path)) || !bytes.Contains(change.After, []byte("custom: 'untouched'")) {
				t.Fatal("receipt or unknown fields changed")
			}
			if tc.changes.Enabled != nil && *tc.changes.Enabled && bytes.Contains(change.After, []byte("enabled:")) {
				t.Fatal("enabling must remove the key")
			}
		})
	}
}

func Test_Update_refuses_invalid_changes_without_writing(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		changes  accountconfig.AccountChanges
		want     error
	}{
		{"empty", "work", accountconfig.AccountChanges{}, accountconfig.ErrNoChanges},
		{"missing", "missing", accountconfig.AccountChanges{Enabled: pointer(false)}, accountconfig.ErrAccountNotFound},
		{"invalid label", "work", accountconfig.AccountChanges{Label: pointer("bad\nlabel")}, accountconfig.ErrInvalidLabel},
		{"duplicate label", "work", accountconfig.AccountChanges{Label: pointer("SIDE")}, accountconfig.ErrDuplicateLabel},
		{"enabled collision", "work", accountconfig.AccountChanges{Enabled: pointer(true)}, accountconfig.ErrDuplicateConfigDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "providers:\n  claude:\n    accounts:\n      - id: work\n        enabled: false\n        config_dir: ~/.claude\n      - id: side\n        config_dir: ~/.claude-side\n"
			target := targetConfig(t, body)
			_, err := accountconfig.Update(context.Background(), target, tc.id, tc.changes)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if !bytes.Equal(fileBytes(t, target.Path), []byte(body)) {
				t.Fatal("rejected update wrote config")
			}
		})
	}
}
