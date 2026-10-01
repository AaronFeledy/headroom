package cursor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_Fetch_maps_usage_summary_when_cookie_pushed(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "WorkosCursorSessionToken=session"})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("WorkosCursorSessionToken=session")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.ProviderName != "Cursor" || got.PrimaryLabel != "Included Plan" || got.SecondaryLabel != "On-Demand" {
		t.Fatalf("labels = %q/%q/%q", got.ProviderName, got.PrimaryLabel, got.SecondaryLabel)
	}
	if got.ReauthCommand != nil || got.NeedsReauth || got.Error != nil {
		t.Fatalf("reauth/error = %v/%v/%v", got.ReauthCommand, got.NeedsReauth, got.Error)
	}
	assertStringPtr(t, got.Subtitle, "pro")
	assertStringPtr(t, got.PrimaryStatusText, "$12.34 / $100 this cycle")
	assertStringPtr(t, got.SecondaryStatusText, "$5 / $20 on-demand")
	if got.Current.Utilization != 0.3 || got.Weekly.Utilization != 25 || !got.ShowSecondary {
		t.Fatalf("usage = current %.2f weekly %.2f show %v", got.Current.Utilization, got.Weekly.Utilization, got.ShowSecondary)
	}
	if got.Current.ResetsAt == nil || got.Current.ResetsAt.UTC().Format(time.RFC3339) != "2026-08-01T12:00:00Z" {
		t.Fatalf("reset = %v", got.Current.ResetsAt)
	}
	if bucketByID(got.Buckets, grokBotBucketID) != nil {
		t.Fatalf("unexpected Grok Bot bucket: %#v", got.Buckets)
	}
}

func Test_Client_Fetch_emits_grok_bot_bucket_when_sand_usage_present(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "cookie", sandUsage: true})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil || got.Error != nil {
		t.Fatalf("fetch/error = %v/%v", err, got.Error)
	}
	bucket := bucketByID(got.Buckets, grokBotBucketID)
	if bucket == nil || bucket.Label != "Grok Bot" || bucket.Utilization != 98.343013 {
		t.Fatalf("grok bot bucket = %#v buckets=%#v", bucket, got.Buckets)
	}
	if bucket.ResetsAt == nil || bucket.ResetsAt.UTC().Format(time.RFC3339) != "2026-08-22T23:57:03Z" {
		t.Fatalf("grok bot reset = %v", bucket.ResetsAt)
	}
	if srv.seenSandMethod != http.MethodPost {
		t.Fatalf("sand method = %q", srv.seenSandMethod)
	}
	if srv.seenSandOrigin != srv.URL {
		t.Fatalf("sand origin = %q, want %q", srv.seenSandOrigin, srv.URL)
	}
}

func Test_Client_Fetch_maps_legacy_request_usage_when_auth_me_has_subject(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "cookie", authSub: "user-123", legacyUsage: true})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.PrimaryLabel != "Requests" || got.Current.Utilization != 50 {
		t.Fatalf("primary = %q %.2f", got.PrimaryLabel, got.Current.Utilization)
	}
	assertStringPtr(t, got.PrimaryStatusText, "10 / 20 requests this cycle")
	if srv.seenLegacyQuery != "user-123" {
		t.Fatalf("legacy user query = %q", srv.seenLegacyQuery)
	}
}

func Test_Client_Fetch_clears_secret_and_needs_reauth_when_unauthorized(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{status: http.StatusUnauthorized})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !got.NeedsReauth || got.Error == nil {
		t.Fatalf("reauth/error = %v/%v", got.NeedsReauth, got.Error)
	}
	if client.hasSecret() {
		t.Fatal("secret was not cleared")
	}
}

func Test_Client_Fetch_reports_error_when_forbidden_without_reauth(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{status: http.StatusForbidden})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.NeedsReauth || got.Error == nil || !client.hasSecret() {
		t.Fatalf("reauth/error/secret = %v/%v/%v", got.NeedsReauth, got.Error, client.hasSecret())
	}
}

func Test_Client_Fetch_reports_error_when_summary_malformed(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "cookie", malformedSummary: true})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.Error == nil || got.NeedsReauth {
		t.Fatalf("error/reauth = %v/%v", got.Error, got.NeedsReauth)
	}
}

func Test_Client_Fetch_honors_canceled_context(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	got, err := client.Fetch(ctx)

	// Then
	if err == nil || got.Error == nil {
		t.Fatalf("err/error = %v/%v", err, got.Error)
	}
}

func Test_Client_SetAccessToken_builds_workos_cookie_from_last_jwt_subject_segment(t *testing.T) {
	// Given
	token := jwtWithSub("auth0|user_abc", "payload")
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "WorkosCursorSessionToken=user_abc%3A%3A" + token})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})

	// When
	err := client.SetAccessToken(token)
	got, fetchErr := client.Fetch(context.Background())

	// Then
	if err != nil || fetchErr != nil || got.Error != nil {
		t.Fatalf("set/fetch/error = %v/%v/%v", err, fetchErr, got.Error)
	}
}

func Test_Client_SetAccessToken_rejects_bad_jwt_without_storing_secret(t *testing.T) {
	// Given
	client := newTestClient(t, Options{})

	// When
	err := client.SetAccessToken("not-a-jwt")

	// Then
	if err == nil || client.hasSecret() {
		t.Fatalf("err/secret = %v/%v", err, client.hasSecret())
	}
}

func Test_Client_Fetch_uses_local_auth(t *testing.T) {
	// Given
	token := jwtWithSub("local-sub", "payload")
	authPath := writeAuthFile(t, token)
	srv := newCursorTestServer(t, cursorTestBehavior{wantCookie: "WorkosCursorSessionToken=local-sub%3A%3A" + token})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client(), AuthPath: authPath})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil || got.Error != nil {
		t.Fatalf("fetch/error = %v/%v", err, got.Error)
	}
}

func Test_Client_Fetch_discovers_local_auth_without_loopback_gate(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, jwtWithSub("local-sub", "payload"))
	srv := newCursorTestServer(t, cursorTestBehavior{})
	client := newTestClient(t, Options{AuthPath: authPath, BaseURL: srv.URL, HTTPClient: srv.Client()})

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.Error != nil || got.NeedsReauth || got.Auth.Source == nil || got.Auth.Source.Kind != "cli" {
		t.Fatalf("error/reauth = %v/%v", got.Error, got.NeedsReauth)
	}
}

func Test_Client_Setters_are_safe_during_concurrent_fetches(t *testing.T) {
	// Given
	srv := newCursorTestServer(t, cursorTestBehavior{})
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup

	// When
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for ctx.Err() == nil {
				client.SetCookieHeader(fmt.Sprintf("cookie-%d", id))
				_, _ = client.Fetch(ctx)
			}
		}(i)
	}
	wg.Wait()

	// Then
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("ctx err = %v", ctx.Err())
	}
}

func assertStringPtr(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("string ptr = %v, want %q", got, want)
	}
}

func bucketByID(buckets []usage.Bucket, id string) *usage.Bucket {
	for index := range buckets {
		if buckets[index].ID == id {
			return &buckets[index]
		}
	}
	return nil
}

func jwtWithSub(sub string, payloadPart string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, sub)))
	if payloadPart == "payload" {
		return "header." + payload + ".sig"
	}
	return "header." + payloadPart + ".sig"
}

func writeAuthFile(t *testing.T, accessToken string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	content := fmt.Sprintf(`{"accessToken":%q,"refreshToken":"redacted"}`, accessToken)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	return path
}

type cursorTestBehavior struct {
	wantCookie       string
	authSub          string
	status           int
	legacyUsage      bool
	malformedSummary bool
	sandUsage        bool
}

type cursorTestServer struct {
	*httptest.Server
	seenLegacyQuery string
	seenSandMethod  string
	seenSandOrigin  string
}

func newCursorTestServer(t *testing.T, behavior cursorTestBehavior) *cursorTestServer {
	t.Helper()
	out := &cursorTestServer{}
	out.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if behavior.wantCookie != "" && r.Header.Get("Cookie") != behavior.wantCookie {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if behavior.status != 0 {
			w.WriteHeader(behavior.status)
			_, _ = w.Write([]byte(`{"error":"status"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/usage-summary":
			if behavior.malformedSummary {
				_, _ = w.Write([]byte(`{"individualUsage":`))
				return
			}
			_, _ = w.Write([]byte(`{"billingCycleEnd":"2026-08-01T12:00:00Z","membershipType":"pro","individualUsage":{"plan":{"used":1234,"limit":10000,"totalPercentUsed":0.3},"onDemand":{"used":500,"limit":2000}}}`))
		case "/api/auth/me":
			if behavior.authSub == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"sub":%q}`, behavior.authSub)))
		case "/api/usage":
			out.seenLegacyQuery = r.URL.Query().Get("user")
			if !behavior.legacyUsage || !strings.EqualFold(r.Method, http.MethodGet) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"gpt-4":{"numRequestsTotal":10,"maxRequestUsage":20}}`))
		case sandUsagePath:
			if !behavior.sandUsage || r.Method != http.MethodPost {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			out.seenSandMethod = r.Method
			out.seenSandOrigin = r.Header.Get("Origin")
			_, _ = w.Write([]byte(`{"currentPeriodStart":"2026-08-15T23:57:03.809Z","nextResetTimestampUtc":"2026-08-22T23:57:03.809Z","usagePercent":98.343013,"hasAvailableUsage":true,"hasNonZeroIncludedLimit":true,"grokPlanLabel":"Grok Bot Plan"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(out.Close)
	return out
}
