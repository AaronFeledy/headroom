package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func Test_BankedMetadata_read_only_polling_and_account_binding(t *testing.T) {
	// Given
	path := writeClaudeCredentials(t, credentialFixture{Access: "fixture-access", Refresh: "fixture-refresh", ExpiresAt: futureMillis(t)})
	var plain, status, profile atomic.Int32
	var failStatus atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("only read-only GETs are permitted")
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("anthropic-beta") != betaHeader || r.Header.Get("User-Agent") != userAgent {
			t.Error("missing usage headers")
		}
		switch r.URL.Path {
		case "/usage":
			if r.URL.RawQuery == "" {
				plain.Add(1)
				writeResponse(t, w, `{"five_hour":{"utilization":96},"seven_day":{"utilization":50},"extra_usage":{"is_enabled":true,"monthly_limit":1000,"used_credits":100,"utilization":10}}`)
				return
			}
			if r.URL.RawQuery != "cedar_ember=1&skip_spend=1" {
				t.Errorf("unexpected query: %s", r.URL.RawQuery)
			}
			status.Add(1)
			if failStatus.Load() {
				w.WriteHeader(429)
				return
			}
			writeResponse(t, w, `{"cedar_ember":{"eligible":true,"next_grant_id":"fixture","grants":[{"id":"fixture","resets_total":4,"resets_left":3,"paused":false,"usable_now":true}]}}`)
		case "/api/oauth/profile":
			profile.Add(1)
			writeResponse(t, w, `{"account":{"uuid":"22222222-2222-2222-2222-222222222222"},"organization":{"uuid":"11111111-1111-1111-1111-111111111111"}}`)
		default:
			t.Errorf("unexpected read path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(Options{CredentialsPath: path, HTTPClient: server.Client(), APIBaseURL: server.URL, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token"})
	// When
	first, err := c.Fetch(context.Background())
	if err != nil || first.Error != nil {
		t.Fatalf("fetch failed: %v %v", err, first.Error)
	}
	second, err := c.Fetch(context.Background())
	// Then
	if err != nil || second.RateLimitResetCredits == nil || second.RateLimitResetCredits.AvailableCount != 3 || second.RateLimitResetCredits.UsableNow == nil || !*second.RateLimitResetCredits.UsableNow {
		t.Fatalf("missing banked metadata: %+v %v", second.RateLimitResetCredits, err)
	}
	if second.ProviderAccountID != "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222" || !matchesAccountFingerprint(second.ProviderAccountID, *second.RateLimitResetCredits.AccountFingerprint) {
		t.Fatal("account binding missing")
	}
	if plain.Load() != 2 || status.Load() != 1 || profile.Load() != 1 {
		t.Fatalf("GET counts plain=%d status=%d profile=%d", plain.Load(), status.Load(), profile.Load())
	}
	if !containsBucket(second.Buckets, "extra") {
		t.Fatal("plain usage lost extra usage")
	}
	// Given a failed status refresh, retain only bounded last-good metadata.
	failStatus.Store(true)
	c.bankMu.Lock()
	c.bank.attemptedAt = time.Now().Add(-61 * time.Second)
	c.bankMu.Unlock()
	// When
	retained, err := c.Fetch(context.Background())
	// Then
	if err != nil || retained.Error != nil || retained.RateLimitResetCredits == nil || status.Load() != 2 {
		t.Fatal("status failure degraded ordinary usage or discarded fresh cached metadata")
	}
	// Given expired last-good metadata.
	c.bankMu.Lock()
	c.bank.goodAt = time.Now().Add(-16 * time.Minute)
	c.bankMu.Unlock()
	// When
	expired, err := c.Fetch(context.Background())
	// Then
	if err != nil || expired.Error != nil || expired.RateLimitResetCredits != nil {
		t.Fatal("expired metadata remained visible or degraded ordinary usage")
	}
}
