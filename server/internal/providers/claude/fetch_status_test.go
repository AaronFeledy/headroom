package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_FetchStatus_tracks_actual_selected_credential_on_outage_recovery_and_rotation(t *testing.T) {
	// Given
	expires := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	path := writeClaudeCredentials(t, credentialFixture{Access: "synthetic-one", Refresh: "synthetic-refresh", ExpiresAt: expires})
	var status atomic.Int32
	status.Store(503)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		writeResponse(t, w, `{"five_hour":{"utilization":20}}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: path, UsageURL: server.URL, TokenURL: server.URL, HTTPClient: server.Client()})
	first, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.FetchFailure == nil || first.FetchFailure.Kind != usage.FailureTransient || first.CredentialEpoch == nil {
		t.Fatal("outage metadata missing")
	}
	status.Store(200)
	recovered, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Error != nil || recovered.CredentialEpoch == nil || *recovered.CredentialEpoch != *first.CredentialEpoch {
		t.Fatal("same credential changed epoch during recovery")
	}
	writeClaudeCredentialsAt(t, path, credentialFixture{Access: "synthetic-two", Refresh: "synthetic-refresh", ExpiresAt: expires})
	mtime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	status.Store(503)
	// When
	rotated, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Then
	if rotated.FetchFailure == nil || rotated.FetchFailure.Kind != usage.FailureTransient || rotated.CredentialEpoch == nil || *rotated.CredentialEpoch == *first.CredentialEpoch {
		t.Fatal("outage used previous credential epoch after rotation")
	}
}
