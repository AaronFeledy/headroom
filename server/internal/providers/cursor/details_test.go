package cursor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGrantInventoryBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		known      bool
		count      int
	}{
		{"valid", `{"grants":[{"displayName":"Included bonus","remainingCents":"1234","totalCents":2000,"expiresAtMs":"1792972800000"}]}`, true, 1},
		{"empty list", `{"grants":[]}`, true, 0},
		{"explicit no grants", `{"hasCreditGrants":false}`, true, 0},
		{"aggregate only", `{"creditBalanceCents":"1234"}`, true, 0},
		{"unknown", `{}`, false, 0},
		{"null", `{"grants":null}`, false, 0},
		{"missing amounts", `{"grants":[{"displayName":"Unknown"}]}`, false, 0},
		{"fractional rejected", `{"grants":[{"remainingCents":1.5}]}`, false, 0},
		{"negative rejected", `{"grants":[{"remainingCents":-1}]}`, false, 0},
		{"unsafe rejected", `{"grants":[{"remainingCents":"9007199254740992"}]}`, false, 0},
		{"int64 overflow rejected", `{"grants":[{"remainingCents":"9223372036854775808"}]}`, false, 0},
		{"malformed expiry rejected", `{"grants":[{"remainingCents":0,"expiresAtMs":"not-a-date"}]}`, false, 0},
		{"contradictory amounts", `{"grants":[{"remainingCents":2,"totalCents":1}]}`, false, 0},
		{"restricted remains unknown applicability", `{"grants":[{"displayName":"Grok only","remainingCents":12,"scopes":["grok"],"restrictions":{"product":"grok"}}]}`, true, 1},
		{"invalid whole list", `{"grants":[{"remainingCents":12},{"remainingCents":"bad"}]}`, false, 0},
		{"oversized whole list", `{"grants":[` + strings.Repeat(`{"remainingCents":1},`, 64) + `{"remainingCents":1}]}`, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given / When
			got := parseGrants([]byte(tt.body))
			// Then
			if (got != nil) != tt.known {
				t.Fatalf("known=%v", got != nil)
			}
			if got == nil {
				return
			}
			if len(got.details.Items) != tt.count {
				t.Fatalf("items=%d", len(got.details.Items))
			}
			for _, row := range got.details.Items {
				if row.Product != nil || row.Scope == nil || row.Scope.Kind != "unknown" || row.Scope.SharedPool != nil {
					t.Fatal("inferred grant applicability")
				}
			}
		})
	}
}

func TestGrantLabelsDoNotExposeIdentity(t *testing.T) {
	for _, label := range []string{"person@example.test", "https://example.test", "user_12345", "user12345", "12345678-abcd-1234-abcd-123456789012", "bad\nlabel", "bad\u202elabel", strings.Repeat("x", 81)} {
		t.Run(label, func(t *testing.T) {
			// Given
			body, err := json.Marshal(struct {
				Grants []grantLabelFixture `json:"grants"`
			}{[]grantLabelFixture{{label, 1}}})
			if err != nil {
				t.Fatal(err)
			}
			// When
			got := parseGrants(body)
			// Then
			if got == nil || got.details.Items[0].DisplayName != "Credit grant" {
				t.Fatal("unsafe cosmetic label retained")
			}
		})
	}
}

type grantLabelFixture struct {
	Name      string `json:"displayName"`
	Remaining int    `json:"remainingCents"`
}

func TestExactWireValues(t *testing.T) {
	for _, raw := range []string{`"9007199254740991"`, `9007199254740991`, `0`, `"0"`} {
		// Given / When
		var got exactMinor
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		// Then
		if got.invalid || got.value == nil {
			t.Fatalf("valid integer rejected: %s", raw)
		}
	}
	for _, raw := range []string{`1.01`, `"1.01"`, `-1`, `9007199254740992`, `"9223372036854775808"`, `true`} {
		// Given / When
		var got exactMinor
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		// Then
		if !got.invalid || got.value != nil {
			t.Fatalf("invalid integer accepted: %s", raw)
		}
	}
}

func TestEpochMillisecondsNeverGuessSeconds(t *testing.T) {
	for _, raw := range []string{`1792972800000`, `"1792972800000"`, `"2026-10-26T00:00:00Z"`} {
		// Given / When
		var got epochMillis
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		// Then
		if got.value == nil || got.value.UnixMilli() != 1792972800000 {
			t.Fatalf("epoch parsed incorrectly: %s", raw)
		}
	}
	// Given / When
	var seconds epochMillis
	if err := json.Unmarshal([]byte(`1792972800`), &seconds); err != nil {
		t.Fatal(err)
	}
	// Then
	if seconds.value == nil || seconds.value.Year() != 1970 {
		t.Fatal("seconds were guessed")
	}
	for _, raw := range []string{`"9223372036854775808"`, `253402300800000`, `-1`, `"bad"`, `1.5`} {
		var got epochMillis
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.value != nil || !got.invalid {
			t.Fatalf("invalid epoch accepted: %s", raw)
		}
	}
}

func TestWeeklyFinancialDetailsUseOnlyReportedPeriod(t *testing.T) {
	// Given
	c := modernClient(t, modernFixture{summary: `{"membershipType":"unified_pro"}`, period: `{"billingCycleStart":1792972800000,"billingCycleEnd":1793577600000,"planUsage":{"totalPercentUsed":37,"totalSpend":"1500","includedSpend":"1234","bonusSpend":"266","remaining":"8766","limit":"10000"},"spendLimitUsage":{"individualUsed":42,"individualLimit":100,"pooledUsed":"87","pooledLimit":"500","limitType":"team"}}`, grants: `{"grants":[{"displayName":"Expired bonus","remainingCents":"100","totalCents":"200","expiresAtMs":"1792972800000"}]}`})
	// When
	got, err := c.Fetch(context.Background())
	// Then
	if err != nil || got.Error != nil {
		t.Fatal("fetch failed")
	}
	items := got.UsageDetails.Finances.Items
	if len(items) != 3 || *items[0].IncludedUsedMinor != 1234 || *items[0].IncludedLimitMinor != 10000 || *items[0].IncludedRemainingMinor != 8766 {
		t.Fatalf("financial values=%#v", items)
	}
	for _, item := range items {
		if item.StartsAt == nil || item.ResetsAt == nil || item.ResetsAt.Sub(*item.StartsAt) != 7*24*time.Hour {
			t.Fatal("wrong financial window")
		}
	}
	grant := got.UsageDetails.CreditGrants.Items[0]
	if grant.ExpiresAt == nil || !grant.ExpiresAt.Before(c.discovery.Now()) || *grant.RemainingMinor != 100 || grant.Product != nil {
		t.Fatal("expired inventory altered")
	}
	if got.RateLimitResetCredits != nil {
		t.Fatal("read-only inventory created reset credits")
	}
}
