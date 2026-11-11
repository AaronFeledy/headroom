package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_BankedMetadata_refresh_cadence(t *testing.T) {
	// The ordinary path never issues its own status GET: the usage response
	// carries it, and last-good metadata simply expires after fifteen minutes.
	cases := []struct {
		name        string
		goodAtAge   time.Duration
		utilization float64
		wantCredits bool
	}{
		{"ordinary cached", 4 * time.Minute, 50, true},
		{"ordinary expired", 16 * time.Minute, 50, false},
		{"near limit cached", 30 * time.Second, 95, true},
		{"near limit old response", 61 * time.Second, 95, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			c := New(Options{UsageURL: server.URL, APIBaseURL: server.URL, HTTPClient: server.Client()})
			total, left := int64(2), int64(2)
			paused, usable := false, true
			c.bank = bankCache{token: "fixture", accountID: "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222", goodAt: time.Now().Add(-tc.goodAtAge), status: &bankStatus{eligible: true, nextGrantID: "a", grants: []bankGrant{{ID: "a", ResetsTotal: &total, ResetsLeft: &left, Paused: &paused, UsableNow: &usable}}}}
			data := usage.UsageData{Buckets: []usage.Bucket{{ID: usage.BucketWeekly, Utilization: tc.utilization}}}
			// When
			c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data, nil)
			// Then
			if calls.Load() != 0 {
				t.Fatalf("auxiliary GETs = %d, want 0", calls.Load())
			}
			if (data.RateLimitResetCredits != nil) != tc.wantCredits {
				t.Fatalf("credits visible = %t, want %t", data.RateLimitResetCredits != nil, tc.wantCredits)
			}
		})
	}
}

func Test_BankedMetadata_only_valid_ineligible_status_releases_pending_gate(t *testing.T) {
	// Given a pending receipt, without invoking any consumption code.
	cases := []struct {
		name    string
		body    string
		blocked bool
	}{
		{"ineligible empty", `{"cedar_ember":{"eligible":false,"grants":[]}}`, false},
		{"ineligible overflow", `{"cedar_ember":{"eligible":false,"grants":[{"id":"a","resets_total":9223372036854775807,"resets_left":9223372036854775807,"paused":false,"usable_now":false},{"id":"b","resets_total":1,"resets_left":1,"paused":false,"usable_now":false}]}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Options{})
			accountID := "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
			c.bank = bankCache{token: "fixture", accountID: accountID}
			seedPendingJournal(t, c, accountID, 2)
			data := usage.UsageData{}
			// When
			c.attachResetCredits(context.Background(), &credentials{accessToken: "fixture"}, &data, parseBankStatus([]byte(tc.body)))
			// Then
			credits := data.RateLimitResetCredits
			if tc.blocked {
				if credits != nil {
					t.Fatal("unknown credits must remain hidden")
				}
			} else if credits == nil || credits.AvailableCount != 0 || credits.UsableNow == nil || *credits.UsableNow || credits.AccountFingerprint == nil {
				t.Fatal("valid ineligible status must expose account-bound zero credits")
			}
			if _, blocked := c.ResetAttemptStatus("new-fixture"); blocked != tc.blocked {
				t.Fatalf("pending gate blocked = %v, want %v", blocked, tc.blocked)
			}
		})
	}
}

func Test_BankedMetadata_profile_failure_clears_binding_on_token_change(t *testing.T) {
	// Given
	var profileCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/profile" {
			t.Error("only the profile GET is permitted")
			w.WriteHeader(405)
			return
		}
		profileCalls.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	c := New(Options{APIBaseURL: server.URL, HTTPClient: server.Client()})
	c.bank = bankCache{token: "previous-token", accountID: "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"}
	status := parseBankStatus([]byte(`{"cedar_ember":{"eligible":true,"next_grant_id":"a","grants":[{"id":"a","resets_total":2,"resets_left":2,"paused":false,"usable_now":true}]}}`))
	data := usage.UsageData{}
	// When
	c.attachResetCredits(context.Background(), &credentials{accessToken: "new-token"}, &data, status)
	// Then
	if profileCalls.Load() != 1 || data.RateLimitResetCredits == nil || data.RateLimitResetCredits.AvailableCount != 2 || data.RateLimitResetCredits.AccountFingerprint != nil || data.ProviderAccountID != "" {
		t.Fatal("profile failure reused a previous token's account binding or lost the count")
	}
}

func Test_BankedMetadata_failed_usage_makes_one_parameterized_request(t *testing.T) {
	// Given
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "cedar_ember=1" {
			t.Errorf("unexpected auxiliary read after failed main usage: %s", r.URL.RawQuery)
		}
		calls.Add(1)
		w.WriteHeader(429)
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, UsageURL: server.URL, APIBaseURL: server.URL, HTTPClient: server.Client()})
	// When
	data, err := c.Fetch(context.Background())
	// Then
	if err != nil || data.Error == nil || data.RateLimitResetCredits != nil || calls.Load() != 1 {
		t.Fatal("failed ordinary usage triggered auxiliary reads or leaked metadata")
	}
}
