package api_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type fakeCache struct {
	mu         sync.Mutex
	entries    map[string]poller.Entry
	refetches  int
	refetchErr error
}

func newFakeCache(entries ...poller.Entry) *fakeCache {
	cache := &fakeCache{entries: map[string]poller.Entry{}}
	for _, entry := range entries {
		cache.entries[strings.ToLower(entry.Data.ProviderName)] = entry
	}
	return cache
}

func newFakeCacheWithError() *fakeCache {
	cache := newFakeCache(entry("Cursor", 0, 0, nil))
	cache.refetchErr = errors.New("upstream failed")
	return cache
}

func (c *fakeCache) Snapshot() []poller.Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]poller.Entry, 0, len(c.entries))
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	return out
}

func (c *fakeCache) Get(name string) (poller.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[strings.ToLower(name)]
	return entry, ok
}

func (c *fakeCache) PollProvider(_ context.Context, name string) (poller.Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refetches++
	entry, ok := c.entries[strings.ToLower(name)]
	return entry, ok, c.refetchErr
}

type panicCache struct{}

func (panicCache) Snapshot() []poller.Entry        { panic("secret panic") }
func (panicCache) Get(string) (poller.Entry, bool) { return poller.Entry{}, false }

type fakeCursor struct {
	sourceName string
	cookie     string
	token      string
	setErr     error
}

func (c *fakeCursor) SetCookieHeader(cookie string) { c.cookie = cookie }
func (c *fakeCursor) SetDesktopCookie(cookie, sourceName string) {
	c.cookie, c.sourceName = cookie, sourceName
}
func (c *fakeCursor) SetAccessToken(token string) error {
	c.token = token
	if strings.Count(token, ".") != 2 {
		return errors.New("invalid token")
	}
	return c.setErr
}

func entry(provider string, current float64, weekly float64, err *string) poller.Entry {
	data := usage.UsageData{ProviderName: provider, Error: err}.WithBuckets([]usage.Bucket{
		{ID: "session", Label: "Current", Utilization: current},
		{ID: "weekly", Label: "Weekly", Utilization: weekly},
	})
	return poller.Entry{Data: data, FetchedAt: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)}
}

func jwtWithSubject(subject string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + subject + `"}`))
	return header + "." + payload + ".sig"
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, want, rec.Body.String())
	}
}

func assertJSON(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("JSON mismatch\ngot  %s\nwant %s", got, want)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
}
