package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/grok"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Grok_optional_web_timeout_preserves_only_fresh_CLI_success(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cliStatus      int
		cancelCaller   bool
		deadlineCaller bool
		wantSuccess    bool
	}{
		{"fresh CLI", 200, false, false, true},
		{"failed CLI", 500, false, false, false},
		{"caller canceled", 200, true, false, false},
		{"caller deadline", 200, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.deadlineCaller {
				deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
				ctx = deadlineCtx
			}
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/billing":
					w.WriteHeader(tc.cliStatus)
					if _, err := fmt.Fprint(w, `{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2030-01-01T00:00:00Z"}}`); err != nil {
						t.Error(err)
					}
				case "/settings":
					if _, err := fmt.Fprint(w, `{}`); err != nil {
						t.Error(err)
					}
				case "/web":
					if tc.cancelCaller {
						cancel()
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
				default:
					t.Errorf("unexpected synthetic path %s", r.URL.Path)
				}
			}))
			defer upstream.Close()
			defer close(release)
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(`{"https://auth.x.ai/oauth2/token":{"key":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":4102444800000}}`), 0600); err != nil {
				t.Fatal(err)
			}
			client := upstream.Client()
			client.Timeout = 100 * time.Millisecond
			provider, err := grok.NewProvider(grok.Options{CredentialsPath: path, HTTPClient: client, BillingURL: upstream.URL + "/billing", SettingsURL: upstream.URL + "/settings", WebBillingURL: upstream.URL + "/web"})
			if err != nil {
				t.Fatal(err)
			}
			provider.SetCookieHeader("sso=synthetic")
			p := New(Options{})
			if err := p.Register(provider, true); err != nil {
				t.Fatal(err)
			}

			// When
			entry, _, err := p.PollProvider(ctx, "Grok")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(entry.Data)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Success bool           `json:"is_success"`
				Buckets []usage.Bucket `json:"buckets"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}

			// Then
			if wire.Success != tc.wantSuccess {
				t.Fatalf("success = %v, want %v", wire.Success, tc.wantSuccess)
			}
			if tc.wantSuccess {
				if len(wire.Buckets) != 1 || wire.Buckets[0].Utilization != 25 {
					t.Fatalf("fresh CLI meters = %+v", wire.Buckets)
				}
				if entry.Data.FetchStatus.FailureKind != nil {
					t.Fatal("optional timeout became a failed attempt")
				}
			} else if len(wire.Buckets) != 0 {
				t.Fatal("failed attempt exposed buckets")
			}
			if entry.Data.CredentialEpoch != nil {
				t.Fatal("mixed credential attempt claimed a single epoch")
			}
		})
	}
}
