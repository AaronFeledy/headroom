package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"gopkg.in/yaml.v3"
)

func Test_Load_account_identity_shared_fixture(t *testing.T) {
	data, err := os.ReadFile("../usage/testdata/account_identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		IDs struct {
			Valid   []string `json:"valid"`
			Invalid []string `json:"invalid"`
		} `json:"account_ids"`
		Labels struct {
			Valid []struct {
				Raw        string `json:"raw"`
				Normalized string `json:"normalized"`
			} `json:"valid"`
			Invalid []string `json:"invalid"`
		} `json:"account_labels"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	type identityCase struct {
		name, id, label, wantLabel string
		invalid                    bool
	}
	type accountYAML struct {
		ID        string `yaml:"id"`
		Label     string `yaml:"label"`
		ConfigDir string `yaml:"config_dir"`
	}
	var cases []identityCase
	for i, id := range fixture.IDs.Valid {
		cases = append(cases, identityCase{name: fmt.Sprintf("valid-id/%d", i), id: id, wantLabel: id})
	}
	for i, id := range fixture.IDs.Invalid {
		cases = append(cases, identityCase{name: fmt.Sprintf("invalid-id/%d", i), id: id, invalid: true})
	}
	for i, label := range fixture.Labels.Valid {
		cases = append(cases, identityCase{name: fmt.Sprintf("valid-label/%d", i), id: "work", label: label.Raw, wantLabel: label.Normalized})
	}
	for i, label := range fixture.Labels.Invalid {
		cases = append(cases, identityCase{name: fmt.Sprintf("invalid-label/%d", i), id: "work", label: label, wantLabel: "work", invalid: strings.TrimSpace(label) != ""})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := struct {
				Providers map[string]struct {
					Accounts []accountYAML `yaml:"accounts"`
				} `yaml:"providers"`
			}{Providers: map[string]struct {
				Accounts []accountYAML `yaml:"accounts"`
			}{"claude": {Accounts: []accountYAML{{tc.id, tc.label, t.TempDir()}}}}}
			encoded, err := yaml.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			path := writeAccountConfig(t, string(encoded))

			cfg, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", path}})

			if tc.invalid {
				if !errors.Is(err, config.ErrInvalidConfig) {
					t.Fatalf("id=%q label=%q: error = %v, want ErrInvalidConfig", tc.id, tc.label, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Providers["claude"].Accounts[0]
			if got.ID != tc.id || got.Label != tc.wantLabel {
				t.Fatalf("identity = %q/%q, want %q/%q", got.ID, got.Label, tc.id, tc.wantLabel)
			}
		})
	}
}
