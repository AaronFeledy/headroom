package grok

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func Test_Provider_Fetch_returns_close_error_when_billing_response_close_fails(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &trackedBody{reader: strings.NewReader(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`), closeErr: errCloseBody},
			Header:     make(http.Header),
		}, nil
	})}
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: client, BillingURL: "https://example.test/billing", SettingsURL: "https://example.test/settings", TokenURL: "https://example.test/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	_, err = provider.Fetch(context.Background())

	// Then
	if !errors.Is(err, errCloseBody) {
		t.Fatalf("Fetch error = %v, want close body error", err)
	}
}

func Test_Provider_Fetch_closes_settings_body_once_when_settings_close_fails(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token": {"key": "token", "refresh_token": "refresh", "expires_at": 1800000000000}}`)
	settingsBody := &trackedBody{reader: strings.NewReader(`{"subscription_tier_display":"SuperGrok"}`), closeErr: errCloseBody}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := io.ReadCloser(&trackedBody{reader: strings.NewReader(`{"config":{"used":{"val":10},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`)})
		if strings.Contains(req.URL.Path, "settings") {
			body = settingsBody
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: client, BillingURL: "https://example.test/billing", SettingsURL: "https://example.test/settings", TokenURL: "https://example.test/token"})
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
	if got := settingsBody.closes.Load(); got != 1 {
		t.Fatalf("settings body closes = %d, want 1", got)
	}
}

var errCloseBody = errors.New("close body")

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackedBody struct {
	reader   io.Reader
	closeErr error
	closes   atomic.Int32
}

func (b *trackedBody) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b *trackedBody) Close() error {
	b.closes.Add(1)
	return b.closeErr
}
