package grok

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Provider_Fetch_adds_weekly_bucket_from_web_credits_config(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000}}`)
	var webRequestBody []byte
	var webCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			if _, err := w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`)); err != nil {
				t.Fatalf("write billing response: %v", err)
			}
		case "/v1/settings":
			if _, err := w.Write([]byte(`{"subscription_tier_display":"SuperGrok Heavy"}`)); err != nil {
				t.Fatalf("write settings response: %v", err)
			}
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			webCookie = r.Header.Get("Cookie")
			var err error
			webRequestBody, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read web request: %v", err)
			}
			body, err := base64.StdEncoding.DecodeString("AAAAAFoKWA0AAIA/EgAaACIMCI2D2tIGEJDPxc8DKgwIjfj+0gYQkM/FzwM6BwgBFQAAgD86AggEQh4IAhIMCI2D2tIGEJDPxc8DGgwIjfj+0gYQkM/FzwNYAWIAaAGAAAAAD2dycGMtc3RhdHVzOjANCg==")
			if err != nil {
				t.Fatalf("DecodeString: %v", err)
			}
			w.Header().Set("Content-Type", "application/grpc-web+proto")
			if _, err := w.Write(body); err != nil {
				t.Fatalf("write web response: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: authPath,
		HTTPClient:      server.Client(),
		BillingURL:      server.URL + "/v1/billing",
		SettingsURL:     server.URL + "/v1/settings",
		TokenURL:        server.URL + "/oauth2/token",
		WebBillingURL:   server.URL + "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(data.Buckets) != 2 {
		t.Fatalf("Buckets = %#v, want credits and weekly", data.Buckets)
	}
	if data.SecondaryLabel != "Weekly" || !data.ShowSecondary || data.Weekly.Utilization != 1 {
		t.Fatalf("legacy weekly fields = label %q shown %t weekly %#v", data.SecondaryLabel, data.ShowSecondary, data.Weekly)
	}
	if data.SecondaryStatusText != nil {
		t.Fatalf("SecondaryStatusText = %q, want nil for Weekly", *data.SecondaryStatusText)
	}
	weekly := data.Buckets[1]
	if weekly.ID != usage.BucketWeekly || weekly.Label != "Weekly" || weekly.Utilization != 1 {
		t.Fatalf("weekly bucket = %#v", weekly)
	}
	wantReset := time.Date(2026, time.July, 21, 18, 35, 57, 972122000, time.UTC)
	if weekly.ResetsAt == nil || !weekly.ResetsAt.Equal(wantReset) {
		t.Fatalf("weekly reset = %v, want %v", weekly.ResetsAt, wantReset)
	}
	if webCookie != "sso=session" {
		t.Fatalf("Cookie = %q, want sso cookie", webCookie)
	}
	if !bytes.Equal(webRequestBody, []byte{0, 0, 0, 0, 0}) {
		t.Fatalf("request body = %v, want empty gRPC-Web frame", webRequestBody)
	}
}

func Test_Provider_Fetch_returns_web_weekly_when_cli_credentials_are_missing(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeLiveWebUsageFixture(t, w)
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: t.TempDir() + "/missing-auth.json",
		HTTPClient:      server.Client(),
		WebBillingURL:   server.URL,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=session")

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil || data.Error != nil {
		t.Fatalf("Fetch = error %v usage error %v, want weekly success", err, data.Error)
	}
	if len(data.Buckets) != 1 || data.Buckets[0].ID != usage.BucketWeekly || data.Buckets[0].Utilization != 1 {
		t.Fatalf("Buckets = %#v, want web weekly", data.Buckets)
	}
}

func Test_Provider_Fetch_clears_web_cookie_when_grpc_trailer_is_unauthenticated(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"token","refresh_token":"refresh","expires_at":1800000000000}}`)
	webCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			if _, err := w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`)); err != nil {
				t.Fatalf("write billing response: %v", err)
			}
		case "/v1/settings":
			if _, err := w.Write([]byte(`{}`)); err != nil {
				t.Fatalf("write settings response: %v", err)
			}
		default:
			webCalls++
			writeGRPCTrailer(t, w, "16")
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", WebBillingURL: server.URL + "/web"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	provider.SetCookieHeader("sso=expired")

	// When
	first, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	second, err := provider.Fetch(context.Background())

	// Then
	if err != nil || len(first.Buckets) != 1 || len(second.Buckets) != 1 {
		t.Fatalf("Fetch results = first %#v second %#v err %v, want Credits fallback", first.Buckets, second.Buckets, err)
	}
	if webCalls != 1 || provider.webCookieHeader() != "" {
		t.Fatalf("web calls = %d cookie = %q, want one call and cleared cookie", webCalls, provider.webCookieHeader())
	}
}

func writeLiveWebUsageFixture(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	body, err := base64.StdEncoding.DecodeString("AAAAAFoKWA0AAIA/EgAaACIMCI2D2tIGEJDPxc8DKgwIjfj+0gYQkM/FzwM6BwgBFQAAgD86AggEQh4IAhIMCI2D2tIGEJDPxc8DGgwIjfj+0gYQkM/FzwNYAWIAaAGAAAAAD2dycGMtc3RhdHVzOjANCg==")
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	w.Header().Set("Content-Type", "application/grpc-web+proto")
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write web response: %v", err)
	}
}

func writeGRPCTrailer(t *testing.T, w http.ResponseWriter, status string) {
	t.Helper()
	payload := []byte("grpc-status:" + status + "\r\n")
	frame := make([]byte, 5+len(payload))
	frame[0] = 0x80
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	w.Header().Set("Content-Type", "application/grpc-web+proto")
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write gRPC trailer: %v", err)
	}
}
