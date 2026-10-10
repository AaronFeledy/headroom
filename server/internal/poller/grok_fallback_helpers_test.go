package poller

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/grok"
	"google.golang.org/protobuf/proto"
)

type grokResponses struct {
	cliStatus, webStatus, tokenStatus int
	cliRetry, webRetry                time.Duration
}

type grokRequestCounts struct {
	total atomic.Int32
	token atomic.Int32
}

func grokRecoveryProvider(t *testing.T, now func() time.Time, responses grokResponses) (*grok.Provider, *grokRequestCounts) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	expires := int64(4102444800000)
	if responses.tokenStatus != 0 {
		expires = 1
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"https://auth.x.ai/oauth2/token":{"key":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":%d}}`, expires)), 0600); err != nil {
		t.Fatal(err)
	}
	counts := &grokRequestCounts{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts.total.Add(1)
		status := http.StatusOK
		body := []byte(`{}`)
		switch r.URL.Path {
		case "/billing":
			status = responses.cliStatus
			if responses.cliRetry > 0 {
				w.Header().Set("Retry-After", now().Add(responses.cliRetry).Format(http.TimeFormat))
			}
			body = []byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"onDemandCap":{"val":0},"billingPeriodEnd":"2030-01-01T00:00:00Z"}}`)
		case "/token":
			counts.token.Add(1)
			status = responses.tokenStatus
			body = []byte(`{"error":"temporarily_unavailable"}`)
			if status == 400 {
				body = []byte(`{"error":"invalid_grant"}`)
			}
		case "/web":
			status = responses.webStatus
			if responses.webRetry > 0 {
				w.Header().Set("Retry-After", now().Add(responses.webRetry).Format(http.TimeFormat))
			}
			if status == http.StatusOK {
				w.Header().Set("Content-Type", "application/grpc-web+proto")
				payload, err := proto.Marshal(&grok.GetGrokCreditsConfigResponse{Config: &grok.GrokCreditsConfig{CreditUsagePercent: 25, CurrentPeriod: &grok.UsagePeriod{Type: grok.UsagePeriodType_USAGE_PERIOD_TYPE_WEEKLY, End: &grok.Timestamp{Seconds: now().Add(7 * 24 * time.Hour).Unix()}}}})
				if err != nil {
					t.Error(err)
					return
				}
				body = make([]byte, 5+len(payload))
				binary.BigEndian.PutUint32(body[1:5], uint32(len(payload)))
				copy(body[5:], payload)
			}
		case "/settings":
		default:
			t.Errorf("unexpected synthetic request: %s", r.URL.Path)
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := grok.NewProvider(grok.Options{CredentialsPath: path, HTTPClient: server.Client(), BillingURL: server.URL + "/billing", SettingsURL: server.URL + "/settings", TokenURL: server.URL + "/token", WebBillingURL: server.URL + "/web", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	provider.SetCookieHeader("sso=synthetic-browser")
	return provider, counts
}
