package usage_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Auth_JSON_frozen_keys_order_nullability_for_all_providers(t *testing.T) {
	// Given
	for _, provider := range []string{"Claude", "Codex", "Cursor", "Grok"} {
		t.Run(provider, func(t *testing.T) {
			data := usage.FromBuckets(provider, nil)
			data.Auth = usage.NewAuth(provider, "signed_in", &usage.AuthSource{Kind: "cli", Name: provider})
			// When
			encoded, err := json.Marshal(data)
			// Then
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(strings.NewReader(string(fields["auth"])))
			if _, err := decoder.Token(); err != nil {
				t.Fatal(err)
			}
			want := []string{"state", "source", "sign_in_command", "sign_in_url", "accepts_browser_credentials", "checked"}
			for _, key := range want {
				token, err := decoder.Token()
				if err != nil || token != key {
					t.Fatalf("key=%v want=%s err=%v", token, key, err)
				}
				var value json.RawMessage
				if err := decoder.Decode(&value); err != nil {
					t.Fatal(err)
				}
				if (key == "sign_in_command" || key == "sign_in_url") && string(value) != "null" {
					t.Fatalf("%s=%s", key, value)
				}
				if key == "checked" && string(value) != "[]" {
					t.Fatalf("checked=%s", value)
				}
			}
			if decoder.More() {
				t.Fatal("extra auth key")
			}
		})
	}
}

func Test_Auth_signed_out_source_null_and_checked_bounded(t *testing.T) {
	// Given
	auth := usage.NewAuth("Cursor", "signed_out", nil)
	auth.Checked = make([]usage.AuthChecked, 20)
	// When
	encoded, err := json.Marshal(auth)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["source"]) != "null" {
		t.Fatal(string(encoded))
	}
	var checked []usage.AuthChecked
	if err := json.Unmarshal(fields["checked"], &checked); err != nil {
		t.Fatal(err)
	}
	if len(checked) != 12 {
		t.Fatal(len(checked))
	}
}
