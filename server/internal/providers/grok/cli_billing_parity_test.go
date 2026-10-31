package grok

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Provider_Fetch_sends_Grok_CLI_billing_query_and_auth_headers(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	requestCh := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			requestCh <- r.Clone(r.Context())
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":40},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing?existing=value", SettingsURL: server.URL + "/v1/settings"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// When
	data, err := provider.Fetch(ctx)

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	request := <-requestCh
	if got := request.URL.Query().Get("format"); got != "credits" {
		t.Errorf("billing format = %q, want credits", got)
	}
	if got := request.URL.Query().Get("existing"); got != "value" {
		t.Errorf("existing billing query = %q, want value", got)
	}
	if got := request.Header.Get("X-XAI-Token-Auth"); got != "xai-grok-cli" {
		t.Errorf("X-XAI-Token-Auth = %q, want xai-grok-cli", got)
	}
	if got := request.Header.Get("x-userid"); got != "user-123" {
		t.Errorf("x-userid = %q, want persisted user-123", got)
	}
}

func Test_Provider_Fetch_uses_CLI_weekly_period_without_browser_fallback(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	var webCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":63.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-07-08T00:00:00Z","end":"2026-07-15T00:00:00Z"}}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/web":
			webCalls.Add(1)
			http.Error(w, "browser fallback should not be called", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	var weekly *usage.Bucket
	for i := range data.Buckets {
		if data.Buckets[i].ID == usage.BucketWeekly {
			weekly = &data.Buckets[i]
		}
	}
	if weekly == nil {
		t.Errorf("Buckets = %#v, want CLI weekly bucket", data.Buckets)
	} else {
		wantReset := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
		if weekly.Utilization != 63.5 || weekly.ResetsAt == nil || !weekly.ResetsAt.Equal(wantReset) {
			t.Errorf("weekly bucket = %#v, want 63.5%% resetting %v", weekly, wantReset)
		}
	}
	if got := webCalls.Load(); got != 0 {
		t.Errorf("browser weekly calls = %d, want 0", got)
	}
}

func Test_Provider_Fetch_uses_zero_for_omitted_CLI_weekly_utilization_without_browser_fallback(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	var webCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-07-15T00:00:00Z"}}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/web":
			webCalls.Add(1)
			http.Error(w, "browser fallback should not be called", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	if len(data.Buckets) != 1 || data.Buckets[0].ID != usage.BucketWeekly || data.Buckets[0].Utilization != 0 {
		t.Fatalf("Buckets = %#v, want zero-percent CLI weekly bucket", data.Buckets)
	}
	if got := webCalls.Load(); got != 0 {
		t.Fatalf("browser weekly calls = %d, want 0", got)
	}
}

func Test_Provider_Fetch_uses_coherent_weekly_legacy_fields_with_CLI_weekly(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":40},"billingPeriodEnd":"2026-08-01T00:00:00Z","creditUsagePercent":63.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-07-15T00:00:00Z"}}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	if data.SecondaryLabel != "Weekly" || !data.ShowSecondary || data.Weekly.Utilization != 63.5 {
		t.Fatalf("legacy weekly fields = label %q shown %t weekly %#v", data.SecondaryLabel, data.ShowSecondary, data.Weekly)
	}
	if data.SecondaryStatusText != nil {
		t.Fatalf("SecondaryStatusText = %q, want nil for Weekly", *data.SecondaryStatusText)
	}
	if len(data.Buckets) != 3 || data.Buckets[0].ID != usage.BucketCredits || data.Buckets[1].ID != usage.BucketWeekly || data.Buckets[2].ID != usage.BucketOnDemand {
		t.Fatalf("Buckets = %#v, want canonical credits, weekly, and on-demand", data.Buckets)
	}
}

func Test_Provider_Fetch_calls_browser_fallback_when_CLI_weekly_period_is_absent(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	var webCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":40},"billingPeriodEnd":"2026-08-01T00:00:00Z","creditUsagePercent":63.5}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/web":
			webCalls.Add(1)
			writeLiveWebUsageFixture(t, w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want success", err, data.Error)
	}
	if got := webCalls.Load(); got != 1 {
		t.Fatalf("browser weekly calls = %d, want 1", got)
	}
	if len(data.Buckets) != 3 || data.Buckets[0].ID != usage.BucketCredits || data.Buckets[1].ID != usage.BucketWeekly || data.Buckets[2].ID != usage.BucketOnDemand {
		t.Fatalf("Buckets = %#v, want credits, browser weekly, and on-demand", data.Buckets)
	}
}

func Test_Provider_Fetch_calls_browser_fallback_when_CLI_weekly_reset_is_invalid(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000,"user_id":"user-123"}}`)
	var webCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":63.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"not-a-timestamp"}}}`))
		case "/web":
			webCalls.Add(1)
			writeLiveWebUsageFixture(t, w)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want browser fallback success", err, data.Error)
	}
	if got := webCalls.Load(); got != 1 {
		t.Fatalf("browser weekly calls = %d, want 1", got)
	}
}

func Test_mapBilling_clamps_CLI_weekly_utilization(t *testing.T) {
	// Given
	data := baseUsageData()
	body := strings.NewReader(`{"config":{"creditUsagePercent":163.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-07-15T00:00:00-05:00"}}}`)

	// When
	err := mapBilling(body, &data, time.Time{})

	// Then
	if err != nil {
		t.Fatalf("mapBilling: %v", err)
	}
	if len(data.Buckets) != 1 || data.Buckets[0].Utilization != 100 {
		t.Fatalf("Buckets = %#v, want weekly utilization clamped to 100", data.Buckets)
	}
	if got := data.Buckets[0].ResetsAt; got == nil || got.Location() != time.UTC || got.Format(time.RFC3339) != "2026-07-15T05:00:00Z" {
		t.Fatalf("weekly reset = %v, want parsed UTC reset", got)
	}
}
