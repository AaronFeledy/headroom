package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAccountIdentitySharedFixture(t *testing.T) {
	data, err := os.ReadFile("../../../server/internal/usage/testdata/account_identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		AccountIDs    struct{ Valid, Invalid []string } `json:"account_ids"`
		AccountLabels struct {
			Valid   []struct{ Raw, Normalized string }
			Invalid []string
		} `json:"account_labels"`
		ProviderTypes struct {
			Valid, Invalid []string
			InvalidValues  []json.RawMessage `json:"invalid_values"`
		} `json:"provider_types"`
		AccountValues struct {
			NullResults []json.RawMessage `json:"null_results"`
		} `json:"account_values"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	marshal := func(value any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	type identityCase struct {
		account, providerType json.RawMessage
		wantAccount           *ProviderAccount
		wantType              string
	}
	var cases []identityCase
	for _, id := range fixture.AccountIDs.Valid {
		for _, label := range fixture.AccountLabels.Valid {
			cases = append(cases, identityCase{account: marshal(map[string]string{"id": id, "label": label.Raw}),
				wantAccount: &ProviderAccount{ID: id, Label: label.Normalized}})
		}
	}
	for _, id := range fixture.AccountIDs.Invalid {
		cases = append(cases, identityCase{account: marshal(map[string]string{"id": id, "label": "Work"})})
	}
	for _, label := range fixture.AccountLabels.Invalid {
		cases = append(cases, identityCase{account: marshal(map[string]string{"id": "work", "label": label})})
	}
	for _, account := range fixture.AccountValues.NullResults {
		cases = append(cases, identityCase{account: account})
	}
	for _, value := range fixture.ProviderTypes.Valid {
		cases = append(cases, identityCase{providerType: marshal(value), wantType: value})
	}
	for _, value := range fixture.ProviderTypes.Invalid {
		cases = append(cases, identityCase{providerType: marshal(value)})
	}
	for _, value := range fixture.ProviderTypes.InvalidValues {
		cases = append(cases, identityCase{providerType: value})
	}
	for index, test := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			row := map[string]json.RawMessage{
				"provider_name": marshal("Claude (Work)"), "error": marshal(nil),
				"is_success": marshal(true), "needs_reauth": marshal(false), "buckets": json.RawMessage(`[]`),
			}
			if test.account != nil {
				row["account"] = test.account
			}
			if test.providerType != nil {
				row["provider_type"] = test.providerType
			}
			providers, err := decodeUsage(marshal([]map[string]json.RawMessage{row}))
			if err != nil || len(providers) != 1 {
				t.Fatalf("snapshot rejected: %v", err)
			}
			wantType := test.wantType
			if wantType == "" {
				wantType = "Claude (Work)"
			}
			if providers[0].Type() != wantType || !reflect.DeepEqual(providers[0].Account, test.wantAccount) {
				t.Fatalf("type/account = %q/%#v, want %q/%#v", providers[0].Type(), providers[0].Account, wantType, test.wantAccount)
			}
		})
	}
}

func TestAccountDashboardPacesByTypeWithSeparateWarningIdentity(t *testing.T) {
	body := []byte(`[{"provider_name":"Claude (Work)","provider_type":"Claude","account":{"id":"work","label":"Work"},"error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"session","label":"Session","utilization":50,"resets_at":"2026-09-11T14:30:00Z"}]}]`)
	options, stdout, _ := baseOptions(body)
	providers, err := decodeUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	warnings := map[string]warningState{}

	text := render(providers, options.Now(), false, warnings)

	if !strings.Contains(text, "On pace") || !strings.Contains(text, "Claude (Work)") {
		t.Fatal(text)
	}
	if _, ok := warnings["claude (work)\x00session"]; !ok || len(warnings) != 1 {
		t.Fatalf("warning identity changed: %#v", warnings)
	}
	if got := pacing(providers[0].Type(), providers[0].Buckets[0], options.Now()); !got.available || got.duration != 5*time.Hour {
		t.Fatalf("account pace = %#v", got)
	}
	if err := Run(context.Background(), options); err != nil || !strings.Contains(stdout.String(), "On pace") {
		t.Fatalf("CLI account dashboard: %s %v", stdout, err)
	}
}

func TestAccountJSONPreservesBridgeMetadata(t *testing.T) {
	for _, weeklyOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(weeklyOnly), func(t *testing.T) {
			body := []byte(`[{"provider_name":"Claude (😀)","provider_type":"Claude","account":{"id":"work","label":"😀"},"future_field":{"enabled":true},"error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"session","label":"Session","utilization":10},{"id":"weekly","label":"Weekly","utilization":20,"future_bucket":42}]}]`)
			options, stdout, _ := baseOptions(body)
			options.Args = []string{"--json"}
			if weeklyOnly {
				options.Args = append(options.Args, "--weekly-only")
			}

			err := Run(context.Background(), options)

			if err != nil {
				t.Fatal(err)
			}
			if !weeklyOnly && !bytes.Equal(bytes.TrimSpace(stdout.Bytes()), body) {
				t.Fatal("raw JSON passthrough changed")
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"provider_type", "account", "future_field"} {
				var original []map[string]json.RawMessage
				if err := json.Unmarshal(body, &original); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(rows[0][field], original[0][field]) {
					t.Fatalf("%s changed: %s", field, rows[0][field])
				}
			}
			var buckets []Bucket
			if err := json.Unmarshal(rows[0]["buckets"], &buckets); err != nil {
				t.Fatal(err)
			}
			if weeklyOnly && (len(buckets) != 1 || buckets[0].ID != "weekly") {
				t.Fatalf("weekly filter: %#v", buckets)
			}
		})
	}
}

func TestWeeklyHeuristicUsesTolerantProviderType(t *testing.T) {
	for _, providerType := range []string{`"Cursor"`, `42`, `""`, `null`} {
		body := []byte(`[{"provider_name":"Cursor (Work)","provider_type":` + providerType + `,"error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"auto","label":"Auto","utilization":10,"starts_at":"2026-10-01T00:00:00Z","resets_at":"2026-10-08T00:00:00Z"}]}]`)
		options, _, _ := baseOptions(body)

		snapshot, providers, err := fetchUsage(context.Background(), options.defaults(), dashboardFlags{weeklyOnly: true})

		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if providerType == `"Cursor"` {
			want = 1
		}
		var raw []struct{ Buckets []Bucket }
		if err := json.Unmarshal(snapshot.Body, &raw); err != nil {
			t.Fatal(err)
		}
		if len(providers[0].Buckets) != want || len(raw[0].Buckets) != want {
			t.Fatalf("type %s: typed/raw weekly filter disagree: %s", providerType, snapshot.Body)
		}
	}
}
