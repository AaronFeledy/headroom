package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type credentialClock struct{ now time.Time }

func (c *credentialClock) Now() time.Time { return c.now }

type swappingProvider struct {
	mu         sync.Mutex
	name       string
	credential string
	calls      int
	deadline   time.Time
	started    chan struct{}
	release    chan struct{}
	set        chan struct{}
}

func (p *swappingProvider) Name() string { return p.name }
func (p *swappingProvider) SetCookieHeader(cookie string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credential = cookie
	if p.set != nil {
		close(p.set)
	}
}
func (p *swappingProvider) SetDesktopCookie(cookie, source string) { p.SetCookieHeader(cookie) }
func (p *swappingProvider) SetAccessToken(token string) error {
	if token == "invalid" {
		return errors.New("invalid synthetic token")
	}
	p.SetCookieHeader(token)
	return nil
}
func (p *swappingProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	p.mu.Lock()
	p.calls++
	credential := p.credential
	p.mu.Unlock()
	if p.started != nil {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return usage.UsageData{}, ctx.Err()
		}
	}
	if credential != "old" {
		return usage.UsageData{ProviderName: p.name}, usage.TransportFailure(errors.New("synthetic network failure"))
	}
	epoch := "old-context"
	data := usage.UsageData{ProviderName: p.name, CredentialEpoch: &epoch, ProviderAccountID: "old-account", RateLimitResetCredits: &usage.RateLimitResetCredits{AvailableCount: 3}, FetchFailure: &usage.FetchFailure{Kind: usage.FailureRateLimited, RetryAfter: p.deadline}}
	return data.WithBuckets([]usage.Bucket{{ID: usage.BucketWeekly, Label: "Weekly", Utilization: 42}}), nil
}

func Test_Credentials_cooldown_invalidates_previous_account_without_fetch(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"Cursor", "cursor", `{"cookie":"new"}`},
		{"Cursor", "cursor", `{"access_token":"new"}`},
		{"Grok", "grok", `{"cookie":"new"}`},
	} {
		t.Run(tc.name+tc.body, func(t *testing.T) {
			// Given
			clock := &credentialClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			provider := &swappingProvider{name: tc.name, credential: "old", deadline: clock.now.Add(time.Hour)}
			p := poller.New(poller.Options{Clock: clock})
			if err := p.Register(provider, true); err != nil {
				t.Fatal(err)
			}
			p.PollProvider(context.Background(), tc.name)
			server := httptest.NewServer(api.NewHandler(api.Options{Cache: p, Poller: p, Cursor: provider, Grok: provider, ProviderNames: []string{tc.name}}))
			defer server.Close()
			req, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/providers/"+tc.path+"/credentials", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}

			// When
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result struct {
				Refetched bool           `json:"refetched"`
				Usage     credentialWire `json:"usage"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}

			// Then
			if resp.StatusCode != 200 || result.Refetched {
				t.Errorf("PUT status=%d refetched=%v", resp.StatusCode, result.Refetched)
			}
			assertPendingCredential(t, result.Usage)
			get, err := server.Client().Get(server.URL + "/api/v1/usage/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			defer get.Body.Close()
			var cached credentialWire
			if err := json.NewDecoder(get.Body).Decode(&cached); err != nil {
				t.Fatal(err)
			}
			assertPendingCredential(t, cached)
			if provider.calls != 1 {
				t.Fatal("credential replacement bypassed Retry-After")
			}
			clock.now = provider.deadline
			entry, _, err := p.PollProvider(context.Background(), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Data.Error == nil || len(entry.Data.Buckets) != 0 || entry.Data.ProviderAccountID != "" || entry.Data.RateLimitResetCredits != nil {
				t.Fatal("new-context failure restored previous account")
			}
		})
	}
}

type credentialWire struct {
	Success     bool               `json:"is_success"`
	Error       *string            `json:"error"`
	Buckets     []usage.Bucket     `json:"buckets"`
	Credits     json.RawMessage    `json:"rate_limit_reset_credits"`
	FetchStatus *usage.FetchStatus `json:"fetch_status"`
}

func assertPendingCredential(t *testing.T, wire credentialWire) {
	t.Helper()
	if wire.Success || wire.Error == nil || wire.Buckets == nil || len(wire.Buckets) != 0 || string(wire.Credits) != "null" {
		t.Fatalf("previous account exposed: %+v", wire)
	}
	if wire.FetchStatus != nil && (wire.FetchStatus.CredentialEpoch != nil || wire.FetchStatus.FetchedAt != "") {
		t.Fatal("pending credentials claimed an old/fresh attempt")
	}
}

func Test_Credentials_wait_for_inflight_fetch_before_replacing_context(t *testing.T) {
	// Given
	provider := &swappingProvider{name: "Grok", credential: "old", deadline: time.Now().Add(time.Hour), started: make(chan struct{}), release: make(chan struct{}), set: make(chan struct{})}
	p := poller.New(poller.Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	fetched := make(chan struct{})
	go func() { p.PollProvider(context.Background(), "Grok"); close(fetched) }()
	<-provider.started
	h := api.NewHandler(api.Options{Cache: p, Poller: p, Grok: provider, ProviderNames: []string{"Grok"}})
	rec := httptest.NewRecorder()
	done := make(chan struct{})

	// When
	go func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/providers/grok/credentials", strings.NewReader(`{"cookie":"new"}`)))
		close(done)
	}()
	select {
	case <-provider.set:
		t.Error("setter ran during old-context fetch")
	case <-time.After(25 * time.Millisecond):
	}
	close(provider.release)
	<-fetched
	<-done

	// Then
	entry, ok := p.Get("Grok")
	if !ok || entry.Data.Error == nil || len(entry.Data.Buckets) != 0 || entry.Data.CredentialEpoch != nil {
		t.Fatal("inflight old-context success survived replacement")
	}
	if provider.calls != 1 {
		t.Fatal("replacement bypassed inflight rate deadline")
	}
}

func Test_Credentials_rejected_replacement_preserves_previous_context(t *testing.T) {
	// Given
	provider := &swappingProvider{name: "Cursor", credential: "old", deadline: time.Now().Add(time.Hour)}
	p := poller.New(poller.Options{})
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	p.PollProvider(context.Background(), "Cursor")
	h := api.NewHandler(api.Options{Cache: p, Poller: p, Cursor: provider, ProviderNames: []string{"Cursor"}})
	rec := httptest.NewRecorder()

	// When
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(`{"access_token":"invalid"}`)))

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected setter status = %d", rec.Code)
	}
	entry, ok := p.Get("Cursor")
	if !ok || entry.Data.Error != nil || entry.Data.ProviderAccountID != "old-account" || len(entry.Data.Buckets) != 1 {
		t.Fatal("rejected replacement invalidated valid context")
	}
	if provider.calls != 1 {
		t.Fatal("rejected replacement triggered a fetch")
	}
}
