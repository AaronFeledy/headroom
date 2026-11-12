package api

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	resetFixtureID          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	resetFixtureFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func decodeResetFixture(t *testing.T, body string, allowKind bool) (codexResetRequest, error) {
	t.Helper()
	return decodeResetRequest(json.NewDecoder(strings.NewReader(body)), allowKind)
}

func Test_decodeResetRequest_accepts_optional_kind_only_when_allowed(t *testing.T) {
	base := `"request_id":"` + resetFixtureID + `","confirmed":true,"account_fingerprint":"` + resetFixtureFingerprint + `"`
	// Claude accepts the key, with or without it.
	request, err := decodeResetFixture(t, `{`+base+`,"kind":"fable"}`, true)
	if err != nil || request.Kind != "fable" || request.RequestID != resetFixtureID || !request.Confirmed {
		t.Fatalf("claude kind decode = %+v, %v", request, err)
	}
	if request.AccountFingerprint != resetFixtureFingerprint {
		t.Fatalf("account fingerprint = %q, want %q", request.AccountFingerprint, resetFixtureFingerprint)
	}
	request, err = decodeResetFixture(t, `{`+base+`}`, true)
	if err != nil || request.Kind != "" {
		t.Fatalf("claude legacy decode = %+v, %v", request, err)
	}
	// Codex rejects the same body.
	if _, err := decodeResetFixture(t, `{`+base+`,"kind":"fable"}`, false); err == nil {
		t.Fatal("codex accepted a kind")
	}
}

func Test_decodeResetRequest_rejects_unknown_and_duplicate_keys(t *testing.T) {
	base := `"request_id":"` + resetFixtureID + `","confirmed":true,"account_fingerprint":"` + resetFixtureFingerprint + `"`
	for _, body := range []string{
		`{` + base + `,"kind":"weekly","extra":1}`,
		`{` + base + `,"kind":"weekly","kind":"fable"}`,
		`{` + base + `,"kind":42}`,
		`{` + base + `,"kind":""}`,
	} {
		if _, err := decodeResetFixture(t, body, true); err == nil {
			t.Fatalf("decoder accepted %s", body)
		}
	}
}
