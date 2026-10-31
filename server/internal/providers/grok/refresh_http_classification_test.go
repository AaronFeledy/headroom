package grok

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func Test_Provider_requestRefresh_classifies_HTTP_failures_by_status_and_OAuth_code(t *testing.T) {
	tests := []struct {
		name             string
		status           int
		body             string
		succeedOnAttempt int32
		wantAttempts     int32
		wantErr          error
		unwantedErr      error
	}{
		{name: "429 exhausts three attempts", status: http.StatusTooManyRequests, body: `{"error":"rate_limited"}`, wantAttempts: 3, wantErr: errRefreshFailed},
		{name: "400 temporarily unavailable recovers on third attempt", status: http.StatusBadRequest, body: `{"error":"temporarily_unavailable"}`, succeedOnAttempt: 3, wantAttempts: 3},
		{name: "bare 400 recovers on retry", status: http.StatusBadRequest, succeedOnAttempt: 2, wantAttempts: 2},
		{name: "malformed 401 recovers on retry", status: http.StatusUnauthorized, body: `<html>try again</html>`, succeedOnAttempt: 2, wantAttempts: 2},
		{name: "generic 503 makes one attempt", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`, wantAttempts: 1, wantErr: errRefreshFailed},
		{name: "503 invalid grant is transient failure", status: http.StatusServiceUnavailable, body: `{"error":"invalid_grant"}`, wantAttempts: 1, wantErr: errRefreshFailed, unwantedErr: errInvalidGrant},
		{name: "400 invalid grant is terminal", status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, wantAttempts: 1, wantErr: errInvalidGrant},
		{name: "401 invalid client is terminal", status: http.StatusUnauthorized, body: `{"error":"invalid_client"}`, wantAttempts: 1, wantErr: errInvalidGrant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempt := attempts.Add(1)
				if tt.succeedOnAttempt > 0 && attempt == tt.succeedOnAttempt {
					_, _ = w.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh"}`))
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			provider, err := newIsolatedProvider(Options{CredentialsPath: writeAuthFile(t, `{}`), HTTPClient: server.Client(), TokenURL: server.URL})
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}

			// When
			_, err = provider.requestRefresh(context.Background(), credentials{refreshToken: "old-refresh"})

			// Then
			if tt.wantErr == nil && err != nil {
				t.Fatalf("requestRefresh: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("requestRefresh error = %v, want %v", err, tt.wantErr)
			}
			if tt.unwantedErr != nil && errors.Is(err, tt.unwantedErr) {
				t.Fatalf("requestRefresh error = %v, must not match %v", err, tt.unwantedErr)
			}
			if got := attempts.Load(); got != tt.wantAttempts {
				t.Fatalf("refresh attempts = %d, want %d", got, tt.wantAttempts)
			}
		})
	}
}
