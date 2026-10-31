package grok

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	errRefreshTransport = errors.New("refresh transport")
	errRefreshRead      = errors.New("refresh read")
)

func Test_Provider_requestRefresh_does_not_retry_post_write_transport_error(t *testing.T) {
	// Given
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var attempts atomic.Int32
	requestRead := make(chan struct{}, refreshMaxAttempts)
	serverDone := make(chan error, 1)
	stopServer := make(chan struct{})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				select {
				case <-stopServer:
					serverDone <- nil
				default:
					serverDone <- acceptErr
				}
				return
			}
			attempts.Add(1)
			req, readErr := http.ReadRequest(bufio.NewReader(conn))
			if readErr == nil {
				_, readErr = io.Copy(io.Discard, req.Body)
				readErr = errors.Join(readErr, req.Body.Close())
			}
			if readErr != nil {
				serverDone <- errors.Join(readErr, conn.Close())
				return
			}
			requestRead <- struct{}{}
			if closeErr := conn.Close(); closeErr != nil {
				serverDone <- closeErr
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stopServer)
		_ = listener.Close()
		select {
		case serverErr := <-serverDone:
			if serverErr != nil {
				t.Errorf("TCP server: %v", serverErr)
			}
		case <-time.After(time.Second):
			t.Error("TCP server did not stop")
		}
	})
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: writeAuthFile(t, `{}`),
		HTTPClient:      &http.Client{Transport: transport},
		TokenURL:        "http://" + listener.Addr().String() + "/token",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// When
	_, err = provider.requestRefresh(ctx, credentials{refreshToken: "old-refresh"})

	// Then
	if err == nil {
		t.Fatal("requestRefresh error = nil, want transport error")
	}
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("TCP server did not read complete request")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("refresh attempts = %d, want 1", got)
	}
}

func Test_Provider_requestRefresh_retries_standard_transport_failure_before_write(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh"}`))
	}))
	t.Cleanup(server.Close)
	var dialAttempts atomic.Int32
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if dialAttempts.Add(1) == 1 {
			return nil, errRefreshTransport
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	provider, err := newIsolatedProvider(Options{
		CredentialsPath: writeAuthFile(t, `{}`),
		HTTPClient:      &http.Client{Transport: transport},
		TokenURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	refreshed, err := provider.requestRefresh(context.Background(), credentials{refreshToken: "old-refresh"})

	// Then
	if err != nil {
		t.Fatalf("requestRefresh: %v", err)
	}
	if refreshed.refreshToken != "new-refresh" {
		t.Fatalf("refresh token = %q, want new-refresh", refreshed.refreshToken)
	}
	if got := dialAttempts.Load(); got != 2 {
		t.Fatalf("dial attempts = %d, want 2", got)
	}
}

func Test_Provider_requestRefresh_does_not_retry_response_body_read_error(t *testing.T) {
	// Given
	var attempts atomic.Int32
	body := &trackedBody{reader: &errorReader{err: errRefreshRead}}
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
		}, nil
	})}
	provider, err := newIsolatedProvider(Options{CredentialsPath: writeAuthFile(t, `{}`), HTTPClient: client, TokenURL: "https://example.test/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// When
	_, err = provider.requestRefresh(context.Background(), credentials{refreshToken: "old-refresh"})

	// Then
	if !errors.Is(err, errRefreshRead) {
		t.Fatalf("requestRefresh error = %v, want read error", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("refresh attempts = %d, want 1", got)
	}
	if got := body.closes.Load(); got != 1 {
		t.Fatalf("response body closes = %d, want 1", got)
	}
}

func Test_Provider_refresh_persists_rotated_credentials_when_complete_body_close_fails(t *testing.T) {
	// Given
	authPath := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"old-token","refresh_token":"old-refresh","expires_at":1}}`)
	var attempts atomic.Int32
	body := &trackedBody{
		reader:   strings.NewReader(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`),
		closeErr: errCloseBody,
	}
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
		}, nil
	})}
	provider, err := newIsolatedProvider(Options{CredentialsPath: authPath, HTTPClient: client, TokenURL: "https://example.test/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	current, err := provider.loadCredentials(context.Background())
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}

	// When
	refreshed, err := provider.refresh(context.Background(), current, refreshReasonProactive)

	// Then
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.accessToken != "new-token" || refreshed.refreshToken != "new-refresh" {
		t.Fatalf("refreshed credentials = access %q refresh %q, want rotated credentials", refreshed.accessToken, refreshed.refreshToken)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("refresh attempts = %d, want 1", got)
	}
	if got := body.closes.Load(); got != 1 {
		t.Fatalf("response body closes = %d, want 1", got)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	persisted, err := parseCredentials(data)
	if err != nil {
		t.Fatalf("parse persisted credentials: %v", err)
	}
	if persisted.accessToken != "new-token" || persisted.refreshToken != "new-refresh" {
		t.Fatalf("persisted credentials = access %q refresh %q, want rotated credentials", persisted.accessToken, persisted.refreshToken)
	}
}

func Test_Provider_requestRefresh_returns_cancellation_during_status_retry_backoff(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestStarted := make(chan struct{})
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts.Add(1)
		close(requestStarted)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	provider, err := newIsolatedProvider(Options{CredentialsPath: writeAuthFile(t, `{}`), HTTPClient: client, TokenURL: "https://example.test/token"})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, refreshErr := provider.requestRefresh(ctx, credentials{refreshToken: "old-refresh"})
		result <- refreshErr
	}()
	<-requestStarted

	// When
	cancel()
	err = <-result

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("requestRefresh error = %v, want context cancellation", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("refresh attempts = %d, want 1", got)
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
