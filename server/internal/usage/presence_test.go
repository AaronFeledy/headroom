package usage_test

import (
	"encoding/json"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_UsageData_AccountEvidence(t *testing.T) {
	source := &usage.AuthSource{Kind: "cli", Name: "Grok CLI"}
	for _, tc := range []struct {
		name string
		data usage.UsageData
		want bool
	}{
		{"empty reading", usage.UsageData{ProviderName: "Grok"}, false},
		{"signed out without a source", usage.UsageData{ProviderName: "Grok", Auth: usage.Auth{State: "signed_out"}}, false},
		{"signed in", usage.UsageData{Auth: usage.Auth{State: "signed_in"}}, true},
		{"expired", usage.UsageData{Auth: usage.Auth{State: "expired"}}, true},
		{"source found while state is signed out", usage.UsageData{Auth: usage.Auth{State: "signed_out", Source: source}}, true},
		{"checked signed in", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "browser", Name: "Firefox", Status: "signed_in"}}}}, true},
		{"checked expired", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "app", Name: "Cursor", Status: "expired"}}}}, true},
		{"checked encrypted", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "app", Name: "Cursor", Status: "encrypted"}}}}, true},
		{"checked locked only", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "browser", Name: "Firefox", Status: "locked"}}}}, false},
		{"checked unreadable only", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "cli", Name: "OpenCode", Status: "unreadable"}}}}, false},
		{"checked signed out only", usage.UsageData{Auth: usage.Auth{Checked: []usage.AuthChecked{{Kind: "browser", Name: "Firefox", Status: "signed_out"}}}}, false},
		{"extra account row", usage.UsageData{Account: &usage.Account{ID: "work", Label: "Work"}, Auth: usage.Auth{State: "signed_out"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.data.AccountEvidence(); got != tc.want {
				t.Fatalf("AccountEvidence() = %v, want %v", got, tc.want)
			}
		})
	}
}

func Test_UsageData_RowKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		data usage.UsageData
		want string
	}{
		{"default row uses the provider type", usage.UsageData{ProviderName: "ChatGPT", ProviderType: "Codex"}, "Codex"},
		{"default row falls back to the provider name", usage.UsageData{ProviderName: "Cursor"}, "Cursor"},
		{"extra account row", usage.UsageData{ProviderName: "Claude (Work)", ProviderType: "Claude", Account: &usage.Account{ID: "work"}}, "Claude:work"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.data.RowKey(); got != tc.want {
				t.Fatalf("RowKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_UsageData_MarshalJSON_emits_account_presence(t *testing.T) {
	errText := "rate limited"
	for _, tc := range []struct {
		name string
		data usage.UsageData
		want string
	}{
		{"computed present", usage.UsageData{ProviderName: "Grok", AccountPresence: usage.PresencePresent}, `"present"`},
		{"computed absent", usage.UsageData{ProviderName: "Grok", AccountPresence: usage.PresenceAbsent}, `"absent"`},
		{"fallback signed in", usage.UsageData{ProviderName: "Grok", Auth: usage.Auth{State: "signed_in"}}, `"present"`},
		{"fallback signed out", usage.UsageData{ProviderName: "Grok", Auth: usage.Auth{State: "signed_out"}}, `"absent"`},
		{"fallback error row", usage.UsageData{ProviderName: "Grok", Error: &errText, Auth: usage.Auth{State: "expired"}}, `"present"`},
		{"unknown value falls back to evidence", usage.UsageData{ProviderName: "Grok", AccountPresence: "stale", Auth: usage.Auth{State: "signed_in"}}, `"present"`},
		{"extra account row without evidence", usage.UsageData{ProviderName: "Claude (Work)", ProviderType: "Claude", Account: &usage.Account{ID: "work", Label: "Work"}}, `"present"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatalf("MarshalJSON returned error: %v", err)
			}
			assertJSONField(t, encoded, "account_presence", tc.want)
		})
	}
}
