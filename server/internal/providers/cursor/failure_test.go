package cursor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_Client_Fetch_reports_error_when_upstream_hangs_until_timeout(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, Options{
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Timeout: time.Nanosecond},
	})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned transport error instead of usage error: %v", err)
	}
	if got.Error == nil || got.NeedsReauth {
		t.Fatalf("error/reauth = %v/%v", got.Error, got.NeedsReauth)
	}
}

func Test_Client_Fetch_treats_redirect_html_as_non_auth_failure(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	client.SetCookieHeader("cookie")

	// When
	got, err := client.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if got.Error == nil || got.NeedsReauth || !client.hasSecret() {
		t.Fatalf("error/reauth/secret = %v/%v/%v", got.Error, got.NeedsReauth, client.hasSecret())
	}
}
