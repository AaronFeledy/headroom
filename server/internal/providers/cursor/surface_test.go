package cursor_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/cursor"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

func TestCursorReadOnlySyntheticSurface(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage-summary":
			fmt.Fprint(w, `{"membershipType":"unified_pro","individualUsage":{"plan":{"autoPercentUsed":0,"apiPercentUsed":0}}}`)
		case "/api/dashboard/get-current-period-usage":
			fmt.Fprint(w, `{"billingCycleStart":"1792972800000","billingCycleEnd":"1793577600000","planUsage":{"autoPercentUsed":12,"apiPercentUsed":0,"totalPercentUsed":37}}`)
		case "/api/dashboard/get-plan-info":
			fmt.Fprint(w, `{"planInfo":{"planName":"Synthetic Pro","includedUsagePeriod":2}}`)
		case "/aiserver.v1.DashboardService/GetClientVisibleCreditGrants":
			fmt.Fprint(w, `{"grants":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	discovery := &cursor.Discovery{Environment: winprofile.Environment{GOOS: "linux", Home: t.TempDir(), Root: t.TempDir(), Env: func(string) string { return "" }}, Now: func() time.Time { return time.UnixMilli(1793200000000).UTC() }}
	c := cursor.NewClient(cursor.Options{BaseURL: srv.URL, RPCBaseURL: srv.URL, HTTPClient: srv.Client(), Discovery: discovery})
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"synthetic"}`)) + ".signature"
	if err := c.SetAccessToken(token); err != nil {
		t.Fatal(err)
	}
	// When
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Success bool `json:"is_success"`
		Buckets []struct {
			ID          string  `json:"id"`
			Percent     float64 `json:"utilization"`
			Measurement string  `json:"measurement"`
		} `json:"buckets"`
		Details struct {
			Grants struct {
				State string            `json:"state"`
				Items []json.RawMessage `json:"items"`
			} `json:"credit_grants"`
		} `json:"usage_details"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	// Then
	if !wire.Success || len(wire.Buckets) != 1 || wire.Buckets[0].ID != "auto" || wire.Buckets[0].Percent != 37 || wire.Buckets[0].Measurement != "percent" || wire.Details.Grants.State != "ok" || wire.Details.Grants.Items == nil {
		t.Fatal("public Fetch/JSON surface did not preserve selected weekly usage and known-empty metadata")
	}
	t.Log("public Cursor Fetch -> JSON: weekly auto=37%, percent measurement, known-empty grants; loopback-only endpoints")
}
