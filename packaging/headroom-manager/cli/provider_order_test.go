package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestGroupProvidersByTypeKeepsFirstOccurrenceAndStableTies(t *testing.T) {
	for _, test := range []struct {
		name      string
		providers []Provider
		want      []string
	}{
		{"accounts", []Provider{{ProviderName: "Claude"}, {ProviderName: "Codex"}, {ProviderName: "Cursor"}, {ProviderName: "Grok"},
			{ProviderName: "Claude (Work)", ProviderType: "Claude"}, {ProviderName: "Claude (Home)", ProviderType: "Claude"}},
			[]string{"Claude", "Claude (Work)", "Claude (Home)", "Codex", "Cursor", "Grok"}},
		{"default disabled", []Provider{{ProviderName: "Codex"}, {ProviderName: "Cursor"}, {ProviderName: "Grok"},
			{ProviderName: "Claude (Work)", ProviderType: "Claude"}}, []string{"Codex", "Cursor", "Grok", "Claude (Work)"}},
		{"first account", []Provider{{ProviderName: "Claude (Work)", ProviderType: "Claude"}, {ProviderName: "Grok"}, {ProviderName: "Claude"}},
			[]string{"Claude (Work)", "Claude", "Grok"}},
		{"empty", nil, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := append([]Provider(nil), test.providers...)
			grouped := groupProvidersByType(test.providers)
			names := make([]string, len(grouped))
			for index, provider := range grouped {
				names[index] = provider.ProviderName
			}
			if !reflect.DeepEqual(names, test.want) || !reflect.DeepEqual(test.providers, original) {
				t.Fatalf("grouped = %v, want %v; input changed = %v", names, test.want, !reflect.DeepEqual(test.providers, original))
			}
		})
	}
}

func TestDashboardGroupsOnlyServerTextAndPreservesJSONOrder(t *testing.T) {
	body := []byte(`[{"provider_name":"Claude","error":null,"is_success":true,"needs_reauth":false,"buckets":[]},{"provider_name":"Grok","error":null,"is_success":true,"needs_reauth":false,"buckets":[]},{"provider_name":"Claude (Work)","provider_type":"Claude","account":{"id":"work","label":"Work"},"error":null,"is_success":true,"needs_reauth":false,"buckets":[]}]`)
	for _, source := range []string{"http", "ssh", "desktop", "fallback"} {
		for _, mode := range []string{"text", "json", "weekly-json"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				options, stdout, _ := baseOptions(body)
				options.Args = []string{"--once", "--plain"}
				switch source {
				case "http":
					options.Args = append(options.Args, "--url", "http://example.test")
				case "ssh":
					options.Args = append(options.Args, "--ssh", "example.test")
					options.RunSSH = func(context.Context, string, []string, []byte) ([]byte, []byte, error) {
						frame, err := json.Marshal(struct {
							Schema int    `json:"schema"`
							Status int    `json:"status"`
							Body   string `json:"body"`
						}{1, 200, base64.StdEncoding.EncodeToString(body)})
						return append(frame, '\n'), nil, err
					}
				case "desktop":
					options.FetchLocal = func(context.Context) (LocalSnapshot, error) {
						return LocalSnapshot{Body: body, Status: "ready"}, nil
					}
				case "fallback":
					options.FetchLocal = func(context.Context) (LocalSnapshot, error) {
						return LocalSnapshot{}, ErrLocalUnavailable
					}
				}
				if mode != "text" {
					options.Args = append(options.Args, "--json")
				}
				if mode == "weekly-json" {
					options.Args = append(options.Args, "--weekly-only")
				}
				if err := Run(context.Background(), options); err != nil {
					t.Fatal(err)
				}
				if mode == "text" {
					want := []string{"Claude", "Claude (Work)", "Grok"}
					if source == "desktop" {
						want = []string{"Claude", "Grok", "Claude (Work)"}
					}
					previous := -1
					for _, name := range want {
						heading := "\n" + name + "\n"
						position := strings.Index(stdout.String(), heading)
						if position <= previous || strings.Count(stdout.String(), heading) != 1 {
							t.Fatalf("unexpected dashboard order: %s", stdout)
						}
						previous = position
					}
				} else if mode == "json" {
					if !bytes.Equal(bytes.TrimSpace(stdout.Bytes()), body) {
						t.Fatalf("JSON order or bytes changed: %s", stdout)
					}
				} else {
					var rows []struct {
						ProviderName string `json:"provider_name"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
						t.Fatal(err)
					}
					want := []struct {
						ProviderName string `json:"provider_name"`
					}{{"Claude"}, {"Grok"}, {"Claude (Work)"}}
					if !reflect.DeepEqual(rows, want) {
						t.Fatalf("weekly JSON order changed: %s", stdout)
					}
				}
			})
		}
	}
}
