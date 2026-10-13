package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Refresh_unavailable_when_service_not_running(t *testing.T) {
	// Given
	h := NewHandler(Options{})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/usage/refresh", nil)
	w := httptest.NewRecorder()
	// When
	h.ServeHTTP(w, r)
	// Then
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

type limitedRefresh struct{}

func (limitedRefresh) RequestRefresh() (poller.RefreshResult, error) {
	return poller.RefreshResult{RetryAfterSeconds: 7}, poller.ErrRefreshRateLimited
}

func (limitedRefresh) PollProvider(context.Context, string) (poller.Entry, bool, error) {
	return poller.Entry{}, false, nil
}

func (limitedRefresh) UpdateCredentials(context.Context, string, func() error) (poller.Entry, bool, error) {
	return poller.Entry{}, false, nil
}

func Test_Refresh_rate_limit_contains_retry_delay_and_header(t *testing.T) {
	// Given
	h := NewHandler(Options{Poller: limitedRefresh{}})
	w := httptest.NewRecorder()
	// When
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/usage/refresh", nil))
	// Then
	var body struct {
		Error             string `json:"error"`
		RetryAfterSeconds int    `json:"retry_after_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 429 || w.Header().Get("Retry-After") != "7" || body.Error != "refresh rate limited" || body.RetryAfterSeconds != 7 {
		t.Fatalf("limited response = %d %+v", w.Code, body)
	}
}

func Test_Refresh_rejects_browser_body_query_method_and_unauthenticated_requests(t *testing.T) {
	// Given
	cases := []struct {
		name, method, path, body, header, value string
		status                                  int
	}{
		{"origin", "POST", "/api/v1/usage/refresh", "", "Origin", "https://example.invalid", 403},
		{"fetch-site", "POST", "/api/v1/usage/refresh", "", "Sec-Fetch-Site", "none", 403},
		{"body", "POST", "/api/v1/usage/refresh", "{}", "", "", 400},
		{"query", "POST", "/api/v1/usage/refresh?x=1", "", "", "", 400},
		{"empty-query", "POST", "/api/v1/usage/refresh?", "", "", "", 400},
		{"method", "GET", "/api/v1/usage/refresh", "", "", "", 405},
		{"bearer", "POST", "/api/v1/usage/refresh", "", "Authorization", "Bearer wrong", 401},
	}
	h := NewHandler(Options{AuthToken: "synthetic-token"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer synthetic-token")
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			// When
			h.ServeHTTP(w, r)
			// Then
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
			if tc.status == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow POST")
			}
		})
	}
}

type refreshProvider struct {
	calls   atomic.Int32
	started chan context.Context
}

func (*refreshProvider) Name() string { return "Synthetic" }
func (p *refreshProvider) Fetch(ctx context.Context) (usage.UsageData, error) {
	p.calls.Add(1)
	p.started <- ctx
	<-ctx.Done()
	return usage.UsageData{}, ctx.Err()
}

func Test_Refresh_real_HTTP_returns_202_while_provider_blocked_and_GET_stays_cached(t *testing.T) {
	// Given
	p := poller.New(poller.Options{})
	provider := &refreshProvider{started: make(chan context.Context, 1)}
	if err := p.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, time.Hour) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var fetchCtx context.Context
	select {
	case fetchCtx = <-provider.started:
	case <-deadline.C:
		t.Fatal("initial provider did not dispatch")
	}
	srv := httptest.NewServer(NewHandler(Options{Cache: p, Poller: p}))
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 5 * time.Second
	// When
	response, err := client.Post(srv.URL+"/api/v1/usage/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result poller.RefreshResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	// Then
	if response.StatusCode != 202 || result.Status != "accepted" || result.RetryAfterSeconds != 15 {
		t.Fatalf("response=%d %+v", response.StatusCode, result)
	}
	if response.Header.Get("Retry-After") != strconv.Itoa(result.RetryAfterSeconds) {
		t.Fatalf("accepted Retry-After = %q, JSON delay = %d", response.Header.Get("Retry-After"), result.RetryAfterSeconds)
	}
	if deadline, ok := fetchCtx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
		t.Fatal("unbounded fetch")
	}
	if fetchCtx.Err() != nil {
		t.Fatal("POST completion canceled service work")
	}
	second, err := client.Post(srv.URL+"/api/v1/usage/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if err := json.NewDecoder(second.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if second.StatusCode != 202 || result.Status != "coalesced" || result.RetryAfterSeconds <= 0 {
		t.Fatalf("second response=%d %+v", second.StatusCode, result)
	}
	if second.Header.Get("Retry-After") != strconv.Itoa(result.RetryAfterSeconds) {
		t.Fatalf("coalesced Retry-After = %q, JSON delay = %d", second.Header.Get("Retry-After"), result.RetryAfterSeconds)
	}
	cached, err := client.Get(srv.URL + "/api/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer cached.Body.Close()
	if cached.StatusCode != 200 || provider.calls.Load() != 1 {
		t.Fatal("GET fetched instead of reading cache")
	}
}
