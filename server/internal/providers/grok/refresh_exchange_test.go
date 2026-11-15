package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func Test_Provider_refresh_finishes_retry_and_reload_after_poll_cancellation(t *testing.T) {
	path := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old","refresh_token":"old-r","expires_at":1}}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			cancel()
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte(`{"error":"rate_limited"}`)); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := w.Write([]byte(`{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	provider, err := newIsolatedProvider(Options{CredentialsPath: path, TokenURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := provider.loadCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := provider.refresh(ctx, stale, refreshReasonUnauthorized)
	if err != nil || current.accessToken != "new" || current.refreshToken != "new-r" || calls.Load() != 2 {
		t.Fatalf("refresh = %+v, %v; attempts = %d", current, err, calls.Load())
	}
	stored, err := parseCredentials(mustRead(t, path))
	if err != nil || stored.accessToken != "new" || stored.refreshToken != "new-r" {
		t.Fatalf("persisted credentials = %+v, %v", stored, err)
	}
}

func Test_Provider_refresh_preserves_either_token_changed_during_exchange(t *testing.T) {
	for _, changed := range []string{"key", "refresh_token"} {
		t.Run(changed, func(t *testing.T) {
			path := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old","refresh_token":"old-r","expires_at":1}}`)
			pair := map[string]string{"key": "old", "refresh_token": "old-r"}
			pair[changed] = "external"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := json.Marshal(map[string]map[string]string{"https://auth.x.ai/oauth2/token": pair})
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Error(err)
				}
				if _, err := w.Write([]byte(`{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			provider, err := newIsolatedProvider(Options{CredentialsPath: path, TokenURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			stale, err := provider.loadCredentials(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			current, err := provider.refresh(context.Background(), stale, refreshReasonUnauthorized)
			if err != nil || current.accessToken != pair["key"] || current.refreshToken != pair["refresh_token"] {
				t.Fatalf("concurrent sign-in replaced: %+v, %v", current, err)
			}
		})
	}
}
