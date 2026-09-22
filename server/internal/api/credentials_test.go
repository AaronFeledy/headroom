package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
)

func Test_CursorCredentials_updates_memory_credentials_and_refetches_once(t *testing.T) {
	// Given
	cache := newFakeCache(entry("Cursor", 10, 20, nil))
	cursor := &fakeCursor{}
	handler := api.NewHandler(api.Options{Cache: cache, Cursor: cursor, Poller: cache, ProviderNames: []string{"Cursor"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(`{"cookie":"WorkosCursorSessionToken=sub::token"}`))

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusOK)
	assertJSON(t, rec, `{"provider":"Cursor","refetched":true,"usage":{"provider_name":"Cursor","primary_label":"Current","secondary_label":"Weekly","show_secondary":true,"subtitle":null,"primary_status_text":null,"secondary_status_text":null,"reauth_command":null,"current":{"utilization":10,"resets_at":null},"weekly":{"utilization":20,"resets_at":null},"buckets":[{"id":"session","label":"Current","utilization":10,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null},{"id":"weekly","label":"Weekly","utilization":20,"resets_at":null,"status_text":null,"starts_at":null,"detail_text":null}],"error":null,"needs_reauth":false,"is_success":true,"rate_limit_reset_credits":null}}`)
	if cursor.cookie != "WorkosCursorSessionToken=sub::token" {
		t.Fatalf("cookie setter = %q", cursor.cookie)
	}
	if cache.refetches != 1 {
		t.Fatalf("refetches = %d, want 1", cache.refetches)
	}
}

func Test_GrokCredentials_updates_web_cookie_and_refetches_once(t *testing.T) {
	// Given
	cache := newFakeCache(entry("Grok", 10, 20, nil))
	grok := &fakeGrok{}
	handler := api.NewHandler(api.Options{Cache: cache, Grok: grok, Poller: cache, ProviderNames: []string{"Grok"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/grok/credentials", strings.NewReader(`{"cookie":"sso=session"}`))

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusOK)
	if grok.cookie != "sso=session" {
		t.Fatalf("cookie setter = %q", grok.cookie)
	}
	if cache.refetches != 1 {
		t.Fatalf("refetches = %d, want 1", cache.refetches)
	}
}

type fakeGrok struct {
	cookie string
}

func (g *fakeGrok) SetCookieHeader(cookie string) {
	g.cookie = cookie
}

func Test_CursorCredentials_accepts_access_token_and_rejects_bad_payloads(t *testing.T) {
	// Given
	validToken := jwtWithSubject("user-1")
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"access token", `{"access_token":"` + validToken + `"}`, http.StatusOK},
		{"malformed", `{`, http.StatusBadRequest},
		{"unknown field", `{"cookie":"x","extra":true}`, http.StatusBadRequest},
		{"empty", `{}`, http.StatusBadRequest},
		{"both", `{"cookie":"x","access_token":"` + validToken + `"}`, http.StatusBadRequest},
		{"empty credential", `{"cookie":"   "}`, http.StatusBadRequest},
		{"invalid access token", `{"access_token":"not-a-jwt"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newFakeCache(entry("Cursor", 10, 20, nil))
			handler := api.NewHandler(api.Options{Cache: cache, Cursor: &fakeCursor{}, Poller: cache, ProviderNames: []string{"Cursor"}})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(tt.body))

			// When
			handler.ServeHTTP(rec, req)

			// Then
			assertStatus(t, rec, tt.status)
		})
	}
}

func Test_CursorCredentials_rejects_oversized_setter_refetch_and_method_failures(t *testing.T) {
	// Given
	tests := []struct {
		name   string
		method string
		body   string
		cursor *fakeCursor
		cache  *fakeCache
		status int
	}{
		{"method", http.MethodPost, `{}`, &fakeCursor{}, newFakeCache(), http.StatusMethodNotAllowed},
		{"oversized", http.MethodPut, `{"cookie":"` + strings.Repeat("x", 1<<20) + `"}`, &fakeCursor{}, newFakeCache(), http.StatusBadRequest},
		{"setter", http.MethodPut, `{"access_token":"bad"}`, &fakeCursor{setErr: errors.New("nope")}, newFakeCache(), http.StatusBadRequest},
		{"refetch", http.MethodPut, `{"cookie":"x"}`, &fakeCursor{}, newFakeCacheWithError(), http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := api.NewHandler(api.Options{Cache: tt.cache, Cursor: tt.cursor, Poller: tt.cache, ProviderNames: []string{"Cursor"}})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/v1/providers/cursor/credentials", strings.NewReader(tt.body))

			// When
			handler.ServeHTTP(rec, req)

			// Then
			assertStatus(t, rec, tt.status)
		})
	}
}

func Test_CursorCredentials_requires_auth_when_token_configured(t *testing.T) {
	// Given
	cache := newFakeCache(entry("Cursor", 10, 20, nil))
	handler := api.NewHandler(api.Options{Cache: cache, Cursor: &fakeCursor{}, Poller: cache, AuthToken: "secret", ProviderNames: []string{"Cursor"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(`{"cookie":"x"}`))

	// When
	handler.ServeHTTP(rec, req)

	// Then
	assertStatus(t, rec, http.StatusUnauthorized)
	if cache.refetches != 0 {
		t.Fatalf("unauthenticated credential PUT refetches = %d, want 0", cache.refetches)
	}
}

func Test_CursorCredentials_serializes_setter_and_refetch_when_requests_overlap(t *testing.T) {
	// Given
	transaction := newBlockingCredentialTransaction()
	handler := api.NewHandler(api.Options{Cache: transaction, Cursor: transaction, Poller: transaction, ProviderNames: []string{"Cursor"}})
	firstDone := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(`{"cookie":"first"}`))
		handler.ServeHTTP(rec, req)
		assertStatus(t, rec, http.StatusOK)
		close(firstDone)
	}()
	<-transaction.firstPollStarted

	secondDone := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(`{"cookie":"second"}`))
		handler.ServeHTTP(rec, req)
		assertStatus(t, rec, http.StatusOK)
		close(secondDone)
	}()

	select {
	case <-transaction.secondSetterCalled:
		t.Fatal("second credential setter ran before first refetch completed")
	case <-transaction.readyToReleaseFirst:
	}
	close(transaction.releaseFirstPoll)
	<-firstDone
	<-secondDone
	transaction.assertOrder(t)
}

type blockingCredentialTransaction struct {
	mu                  sync.Mutex
	order               []string
	setters             int
	polls               int
	firstPollStarted    chan struct{}
	releaseFirstPoll    chan struct{}
	secondSetterCalled  chan struct{}
	readyToReleaseFirst <-chan time.Time
}

func newBlockingCredentialTransaction() *blockingCredentialTransaction {
	return &blockingCredentialTransaction{
		firstPollStarted:    make(chan struct{}),
		releaseFirstPoll:    make(chan struct{}),
		secondSetterCalled:  make(chan struct{}),
		readyToReleaseFirst: time.After(25 * time.Millisecond),
	}
}

func (t *blockingCredentialTransaction) SetCookieHeader(cookie string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setters++
	t.order = append(t.order, "set:"+cookie)
	if t.setters == 2 {
		close(t.secondSetterCalled)
	}
}

func (t *blockingCredentialTransaction) SetAccessToken(string) error { return nil }

func (t *blockingCredentialTransaction) Snapshot() []poller.Entry { return nil }

func (t *blockingCredentialTransaction) Get(string) (poller.Entry, bool) {
	return entry("Cursor", 1, 2, nil), true
}

func (t *blockingCredentialTransaction) PollProvider(context.Context, string) (poller.Entry, bool, error) {
	t.mu.Lock()
	t.polls++
	pollNumber := t.polls
	t.order = append(t.order, "poll")
	if pollNumber == 1 {
		close(t.firstPollStarted)
	}
	t.mu.Unlock()
	if pollNumber == 1 {
		<-t.releaseFirstPoll
	}
	return entry("Cursor", 1, 2, nil), true, nil
}

func (t *blockingCredentialTransaction) assertOrder(tb testing.TB) {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	want := []string{"set:first", "poll", "set:second", "poll"}
	if strings.Join(t.order, ",") != strings.Join(want, ",") {
		tb.Fatalf("transaction order = %v, want %v", t.order, want)
	}
}
