package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_BankedMetadata_incomplete_grants_keep_pending_gate(t *testing.T) {
	// Seed an unresolved request directly. Only the parsed response status is
	// attached; no consumer, reset route, reset transport, or real credentials
	// are involved.
	valid := `{"id":"a","resets_total":1,"resets_left":1,"paused":false,"usable_now":true}`
	cases := []struct{ name, next, grants string }{
		{"malformed selected grant", `"next_grant_id":"b",`, valid + `,{"id":"b","resets_total":2,"resets_left":2,"paused":false,"usable_now":"true"}`},
		{"malformed other grant", `"next_grant_id":"a",`, valid + `,{"id":"b","resets_total":2,"resets_left":2,"paused":false,"usable_now":false,"label":42}`},
		{"duplicate grant", `"next_grant_id":"a",`, valid + `,` + valid},
		{"null grant", `"next_grant_id":"a",`, valid + `,null`},
		{"non-string grant type", `"next_grant_id":"a",`, valid + `,{"id":"b","resets_total":2,"resets_left":2,"paused":false,"usable_now":false,"type":42}`},
		{"unknown selected grant", `"next_grant_id":"missing",`, valid},
		{"missing selected grant", "", valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"cedar_ember":{"eligible":true,%s"grants":[%s]}}`, tc.next, tc.grants)
			c := New(Options{})
			accountID := "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
			c.bank = bankCache{token: "fixture", accountID: accountID}
			seedPendingJournal(t, c, accountID, 3)
			data := usage.UsageData{}
			c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data, parseBankStatus([]byte(body)))
			if c.bank.status != nil || data.RateLimitResetCredits != nil {
				t.Fatal("incomplete grants must remain unknown, not imply depletion or refusal")
			}
			if _, blocked := c.ResetAttemptStatus("new-fixture"); !blocked {
				t.Fatal("incomplete grants released the pending gate")
			}
		})
	}
}

func Test_BankedMetadata_zero_credits_preserve_verified_binding(t *testing.T) {
	cases := []struct{ name, body string }{
		{"eligible empty", `{"cedar_ember":{"eligible":true,"grants":[]}}`},
		{"ineligible empty", `{"cedar_ember":{"eligible":false,"grants":[]}}`},
		{"exhausted grant", `{"cedar_ember":{"eligible":true,"grants":[{"id":"a","resets_total":1,"resets_left":0,"paused":false,"usable_now":false}]}}`},
	}
	for _, tc := range cases {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", tc.name, cached), func(t *testing.T) {
				var profileCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/profile" || r.URL.RawQuery != "" {
						t.Error("only the read-only profile GET is permitted")
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					profileCalls.Add(1)
					writeResponse(t, w, `{"account":{"uuid":"22222222-2222-2222-2222-222222222222"},"organization":{"uuid":"11111111-1111-1111-1111-111111111111"}}`)
				}))
				defer server.Close()
				c := New(Options{APIBaseURL: server.URL, HTTPClient: server.Client()})
				accountID := "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
				c.bank = bankCache{token: "fixture"}
				if cached {
					c.bank.accountID = accountID
				}
				seedPendingJournal(t, c, accountID, 1)
				data := usage.UsageData{}
				c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data, parseBankStatus([]byte(tc.body)))
				credits := data.RateLimitResetCredits
				if credits == nil || credits.AvailableCount != 0 || credits.UsableNow == nil || *credits.UsableNow {
					t.Fatal("valid empty status must expose authoritative zero and refusal")
				}
				if data.ProviderAccountID != accountID || credits.AccountFingerprint == nil || !matchesAccountFingerprint(accountID, *credits.AccountFingerprint) {
					t.Fatal("zero credits lost verified account binding")
				}
				if _, blocked := c.ResetAttemptStatus("new-fixture"); blocked {
					t.Fatal("fresh account-bound zero status did not release pending gate")
				}
				wantProfileCalls := 1
				if cached {
					wantProfileCalls = 0
				}
				if profileCalls.Load() != int32(wantProfileCalls) {
					t.Fatalf("profile GETs = %d, want %d", profileCalls.Load(), wantProfileCalls)
				}
				encoded, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					Credits *usage.RateLimitResetCredits `json:"rate_limit_reset_credits"`
				}
				if json.Unmarshal(encoded, &decoded) != nil || decoded.Credits == nil || decoded.Credits.AccountFingerprint == nil || *decoded.Credits.AccountFingerprint != *credits.AccountFingerprint {
					t.Fatal("zero-credit account fingerprint did not survive API serialization")
				}
			})
		}
	}
}

func Test_BankedMetadata_zero_credits_do_not_reuse_previous_token_binding(t *testing.T) {
	var profileCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/profile" || r.URL.RawQuery != "" {
			t.Error("only the read-only profile GET is permitted")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		profileCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := New(Options{APIBaseURL: server.URL, HTTPClient: server.Client()})
	accountID := "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
	c.bank = bankCache{token: "previous-token", accountID: accountID}
	seedPendingJournal(t, c, accountID, 1)
	data := usage.UsageData{}
	c.attachResetCredits(context.Background(), &credentials{accessToken: "new-token"}, &data, parseBankStatus([]byte(`{"cedar_ember":{"eligible":true,"grants":[]}}`)))
	if profileCalls.Load() != 1 || data.RateLimitResetCredits == nil || data.RateLimitResetCredits.AccountFingerprint != nil || data.ProviderAccountID != "" {
		t.Fatal("failed profile lookup reused another token's zero-credit binding")
	}
	if _, blocked := c.ResetAttemptStatus("new-fixture"); !blocked {
		t.Fatal("unbound zero-credit reading released the previous account's gate")
	}
}
