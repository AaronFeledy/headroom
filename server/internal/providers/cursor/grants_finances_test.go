package cursor

import (
	"context"
	"testing"
	"time"
)

func TestGrantAggregateNeverCreatesPrepaidBalance(t *testing.T) {
	for _, period := range []string{"", weeklyPeriodFixture} {
		name := "legacy"
		if period != "" {
			name = "weekly"
		}
		for _, listed := range []bool{false, true} {
			grantCase := "aggregate only"
			if listed {
				grantCase = "individual grants plus aggregate"
			}
			t.Run(name+"/"+grantCase, func(t *testing.T) {
				// Given: a missing list falls back to the restricted-credit aggregate.
				fixture := modernFixture{
					summary: classicSummaryFixture,
					period:  period,
					balance: `{"creditBalanceCents":1200,"hasCreditGrants":true}`,
				}
				if listed {
					fixture.grants = `{"creditBalanceCents":1200,"hasCreditGrants":true,"grants":[{"displayName":"Included bonus","remainingCents":200,"totalCents":500,"expiresAtMs":1793577600000},{"displayName":"Expired bonus","remainingCents":0,"totalCents":100,"expiresAtMs":1792972800000}]}`
				}
				c := modernClient(t, fixture)

				// When
				got, err := c.Fetch(context.Background())

				// Then: only reported individual grants survive; no cash is invented.
				if err != nil || got.Error != nil || got.UsageDetails == nil {
					t.Fatal("fetch failed")
				}
				if got.UsageDetails.Finances != nil {
					t.Error("restricted-credit aggregate created financial items")
				}
				grants := got.UsageDetails.CreditGrants
				if grants == nil || grants.ObservedAt == nil || !grants.ObservedAt.Equal(c.discovery.Now()) {
					t.Fatal("grant observation missing")
				}
				if !listed {
					if grants.State != "unknown" || grants.Items != nil {
						t.Fatal("aggregate fabricated individual grants, count, or expiry")
					}
					return
				}
				if grants.State != "ok" || len(grants.Items) != 2 {
					t.Fatal("individual grant inventory changed")
				}
				for i, want := range []struct {
					name             string
					remaining, total int64
					expires          int64
				}{
					{"Included bonus", 200, 500, 1793577600000},
					{"Expired bonus", 0, 100, 1792972800000},
				} {
					grant := grants.Items[i]
					if grant.DisplayName != want.name || grant.Currency != "USD" || grant.RemainingMinor == nil || *grant.RemainingMinor != want.remaining || grant.TotalMinor == nil || *grant.TotalMinor != want.total || grant.ExpiresAt == nil || !grant.ExpiresAt.Equal(time.UnixMilli(want.expires)) {
						t.Errorf("individual grant %d lost reported values", i)
					}
					if grant.Product != nil || grant.Scope == nil || grant.Scope.Kind != "unknown" || grant.Scope.SharedPool != nil {
						t.Errorf("individual grant %d gained inferred scope", i)
					}
				}
			})
		}
	}
}
