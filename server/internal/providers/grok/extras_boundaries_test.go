package grok

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestBrowserSelectedUsageDropsUnprovenCLIDetails(t *testing.T) {
	var extraCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			_, _ = w.Write([]byte(`{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"prepaidBalance":{"val":1000},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
		case "/settings":
			_, _ = w.Write([]byte(`{}`))
		case "/web":
			writeLiveWebUsageFixture(t, w)
		default:
			extraCalls.Add(1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	path := writeAuthFile(t, `{"https://auth.x.ai/oauth2/token":{"key":"synthetic-token","expires_at":1800000000000}}`)
	p, err := newIsolatedProvider(Options{CredentialsPath: path, HTTPClient: server.Client(), BillingURL: server.URL + "/primary", SettingsURL: server.URL + "/settings", WebBillingURL: server.URL + "/web", PlainBillingURL: server.URL + "/ledger", AutoTopUpURL: server.URL + "/topup", UserURL: server.URL + "/user"})
	if err != nil {
		t.Fatal(err)
	}
	p.SetCookieHeader("sso=synthetic-browser")
	data, err := p.Fetch(context.Background())
	if err != nil || data.Error != nil || !hasWeekly(data) || data.UsageDetails != nil || extraCalls.Load() != 0 {
		t.Fatal("unproven CLI metadata attached to browser selection")
	}
}

func TestCachedInventoryReclassifiesExpiryWithoutChangingObservation(t *testing.T) {
	// Given a last-good observation whose windows have since expired.
	now := time.Unix(1000, 0).UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Minute)
	reported, valid := int64(2), int64(2)
	source := &usage.UsageDetails{ResetInventory: &usage.ResetInventoryDetails{Observation: observation(now), ReportedCount: &reported, ValidNowCount: &valid, Windows: []usage.ResetWindow{{Count: 2, ValidFrom: &start, ExpiresAt: &end}}}}
	target := &usage.UsageDetails{}
	// When the optional read fails.
	mergeExtras(target, source.Clone(), true, now.Add(2*time.Minute))
	// Then only historical inventory remains, never a current validity claim.
	if target.ResetInventory == nil || target.ResetInventory.ValidNowCount != nil || target.ResetInventory.ReportedCount == nil || *target.ResetInventory.ReportedCount != 2 || !target.ResetInventory.ObservedAt.Equal(now) || target.ResetInventory.State != usage.DetailStale || !reflect.DeepEqual(target.ResetInventory.Windows, source.ResetInventory.Windows) {
		t.Fatal("cached expiry or observation incorrect")
	}
}

func TestCachedInventoryDerivesCurrentValidityWhenOK(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end time.Duration
		want       int64
	}{
		{"before future start", 2 * time.Minute, 3 * time.Minute, 0},
		{"at future start", time.Minute, 3 * time.Minute, 2},
		{"at expiry", -time.Minute, time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given cached inventory with an original observation time.
			now := time.Unix(1000, 0).UTC()
			start, end := now.Add(tc.start), now.Add(tc.end)
			reported := int64(2)
			source := &usage.UsageDetails{ResetInventory: &usage.ResetInventoryDetails{Observation: observation(now), ReportedCount: &reported, Windows: []usage.ResetWindow{{Count: 2, ValidFrom: &start, ExpiresAt: &end}}}}
			target := &usage.UsageDetails{}
			// When the still-OK cache is merged at the current clock time.
			mergeExtras(target, source.Clone(), false, now.Add(time.Minute))
			// Then validity reflects that time without rewriting the observation.
			got := target.ResetInventory
			if got == nil || got.ValidNowCount == nil || *got.ValidNowCount != tc.want || got.State != usage.DetailOK || !got.ObservedAt.Equal(now) || !reflect.DeepEqual(got.Windows, source.ResetInventory.Windows) || got.ReportedCount == nil || *got.ReportedCount != 2 {
				t.Fatalf("incorrect cached inventory: %#v", got)
			}
		})
	}
}

func TestCachedInventoryClearsCurrentClaimWhenNotOK(t *testing.T) {
	for _, state := range []string{usage.DetailStale, usage.DetailUnknown, usage.DetailUnsupported} {
		t.Run(state, func(t *testing.T) {
			// Given a non-OK observation, even with currently active windows.
			now := time.Unix(1000, 0).UTC()
			start, end := now.Add(-time.Minute), now.Add(time.Minute)
			count := int64(2)
			source := &usage.UsageDetails{ResetInventory: &usage.ResetInventoryDetails{Observation: usage.Observation{State: state, ObservedAt: &now}, ReportedCount: &count, ValidNowCount: &count, Windows: []usage.ResetWindow{{Count: 2, ValidFrom: &start, ExpiresAt: &end}}}}
			target := &usage.UsageDetails{}
			// When merged without another read failure.
			mergeExtras(target, source.Clone(), false, now)
			// Then it cannot regain a current validity claim from window arithmetic.
			got := target.ResetInventory
			if got == nil || got.ValidNowCount != nil || got.State != state || !got.ObservedAt.Equal(now) || !reflect.DeepEqual(got.Windows, source.ResetInventory.Windows) || got.ReportedCount == nil || *got.ReportedCount != 2 {
				t.Fatalf("incorrect historical inventory: %#v", got)
			}
		})
	}
}

func TestOptionalMalformedResponseDoesNotReplaceLastGood(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	for _, decode := range []func([]byte, time.Time) (*usage.UsageDetails, error){decodeMonetary, decodeTopUp, decodeUser} {
		for _, body := range []string{`null`, `[]`, `{"config":null}`, `{"rule":{"enabled":"true"}}`, `{"userId":1}`, `{`} {
			if details, err := decode([]byte(body), now); err == nil || details != nil {
				t.Fatal("malformed optional response accepted")
			}
		}
	}
}

func TestOptionalRequestsHaveBoundedChildDeadlines(t *testing.T) {
	f := newEnrichmentFixture(t)
	base := f.provider.httpClient.Transport
	var bounded atomic.Int32
	f.provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/ledger" || req.URL.Path == "/topup" || req.URL.Path == "/user" {
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) > 2*time.Second {
				t.Error("unbounded optional request")
			} else {
				bounded.Add(1)
			}
		}
		return base.RoundTrip(req)
	})
	f.fetch(t)
	if bounded.Load() != 3 {
		t.Fatal("missing optional deadline evidence")
	}
}
