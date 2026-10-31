package grok

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func Test_Provider_Fetch_refreshes_once_and_retries_billing_when_billing_returns_unauthorized(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "old-token", "refresh_token": "old-refresh", "expires_at": 1800000000000}}`)
	var billingCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			call := billingCalls.Add(1)
			if call == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
				t.Fatalf("retry Authorization = %q, want Bearer new-token", got)
			}
			_, _ = w.Write([]byte(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
	if got := billingCalls.Load(); got != 2 {
		t.Fatalf("billing calls = %d, want 2", got)
	}
}

func Test_Provider_Fetch_reloads_changed_auth_file_before_refresh_when_billing_returns_unauthorized(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "old-token", "refresh_token": "old-refresh", "expires_at": 1800000000000}}`)
	var billingCalls atomic.Int32
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			call := billingCalls.Add(1)
			if call == 1 {
				if err := os.WriteFile(authPath, []byte(`{"https://auth.x.ai/oauth2/token": {"key": "external-token", "refresh_token": "external-refresh", "expires_at": 1800000000000}}`), 0o600); err != nil {
					t.Fatalf("write changed auth file: %v", err)
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer external-token" {
				t.Fatalf("retry Authorization = %q, want Bearer external-token", got)
			}
			_, _ = w.Write([]byte(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/oauth2/token":
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	data, err := provider.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q, want nil", *data.Error)
	}
	if got := billingCalls.Load(); got != 2 {
		t.Fatalf("billing calls = %d, want 2", got)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("token calls = %d, want 0", got)
	}
}

func Test_Provider_Fetch_refreshes_once_when_concurrent_billing_requests_return_unauthorized(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "old-token", "refresh_token": "old-refresh", "expires_at": 1800000000000}}`)
	var tokenCalls atomic.Int32
	var oldToken401s atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			if r.Header.Get("Authorization") == "Bearer old-token" {
				oldToken401s.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/oauth2/token":
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: server.Client(), BillingURL: server.URL + "/v1/billing", SettingsURL: server.URL + "/v1/settings", TokenURL: server.URL + "/oauth2/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			data, err := provider.Fetch(context.Background())
			if err != nil {
				errCh <- err
				return
			}
			if data.Error != nil {
				errCh <- fmt.Errorf("UsageData.Error = %q", *data.Error)
			}
		}()
	}

	// When
	close(start)
	wg.Wait()
	close(errCh)

	// Then
	for err := range errCh {
		t.Fatalf("Fetch failed: %v", err)
	}
	if got := oldToken401s.Load(); got == 0 {
		t.Fatalf("old-token unauthorized calls = %d, want at least 1", got)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}
}
