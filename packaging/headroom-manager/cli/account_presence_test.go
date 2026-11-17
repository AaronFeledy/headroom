package cli

import (
	"strings"
	"testing"
)

func Test_CLI_render_hides_providers_the_server_found_no_account_for(t *testing.T) {
	// Given
	body := `[
{"provider_name":"Claude","account_presence":"present","error":null,"is_success":true,"needs_reauth":false,
 "buckets":[{"id":"session","label":"Session","utilization":12,"resets_at":null}],
 "auth":{"state":"signed_in","sign_in_command":null,"sign_in_url":null}},
{"provider_name":"Codex","account_presence":"present","error":"AUTH_EXPIRED","is_success":false,"needs_reauth":true,"buckets":[],
 "auth":{"state":"expired","sign_in_command":"codex login","sign_in_url":null}},
{"provider_name":"Cursor","account_presence":7,"error":"Not signed in to Cursor.","is_success":false,"needs_reauth":true,"buckets":[],
 "auth":{"state":"signed_out","sign_in_command":"cursor-agent login","sign_in_url":null}},
{"provider_name":"Grok","account_presence":"absent","error":"Grok auth error. Run ` + "`grok login`" + `.","is_success":false,"needs_reauth":false,"buckets":[],
 "auth":{"state":"signed_out","sign_in_command":"grok login","sign_in_url":null}},
{"provider_name":"OpenCodeGo","error":"OpenCode Go is not signed in.","is_success":false,"needs_reauth":false,"buckets":[],
 "auth":{"state":"signed_out","sign_in_command":"opencode auth login","sign_in_url":null}}
]`
	providers, err := decodeUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	// When
	output := render(providers, Options{}.defaults().Now(), false, map[string]warningState{})
	// Then: an absent row is hidden; a lost sign-in, a malformed value, and an
	// older server's missing field all stay visible.
	if strings.Contains(output, "Grok") || strings.Contains(output, "grok login") {
		t.Fatal(output)
	}
	for _, want := range []string{"Claude", "ChatGPT", "Run: codex login", "Cursor", "OpenCode Go"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in\n%s", want, output)
		}
	}
}

func Test_CLI_render_lists_sign_ins_when_no_provider_has_an_account(t *testing.T) {
	// Given
	command, url, grok := "cursor-agent login", "https://cursor.com/login", "grok login"
	providers := []Provider{
		{ProviderName: "Cursor", AccountAbsent: true, Auth: &ProviderAuth{State: "signed_out", SignInCommand: &command, SignInURL: &url}},
		{ProviderName: "Grok", AccountAbsent: true, Auth: &ProviderAuth{State: "signed_out", SignInCommand: &grok}},
	}
	warnings := map[string]warningState{"claude\x00session": {level: 2}}
	// When
	output := render(providers, Options{}.defaults().Now(), false, warnings)
	// Then
	want := "No provider sign-ins found yet. Sign in to one of these and it appears here:\n" +
		"  Cursor       Run: cursor-agent login · Sign in: https://cursor.com/login\n" +
		"  Grok         Run: grok login\n"
	if !strings.HasSuffix(output, want) || len(warnings) != 0 {
		t.Fatalf("warnings=%v output=\n%s", warnings, output)
	}
}
