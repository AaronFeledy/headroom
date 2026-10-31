package grok

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestBillingPreservesWeeklyQuotaWhenWrappersAreStrings(t *testing.T) {
	// Given string-encoded protobuf scalars alongside authoritative weekly usage.
	body := `{"onDemandEnabled":true,"config":{"creditUsagePercent":63.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-08-01T00:00:00Z"},"prepaidBalance":{"val":"1250"},"onDemandUsed":{"val":"25"},"onDemandCap":{"val":"100"}}}`
	data := baseUsageData()
	// When the primary billing response is mapped.
	err := mapBilling(strings.NewReader(body), &data, time.Unix(1, 0))
	// Then the quota survives and money retains exact minor units.
	if err != nil {
		t.Fatal(err)
	}
	if !hasWeekly(data) || data.Current.Utilization != 63.5 || data.Weekly.Utilization != 63.5 || len(data.Buckets) != 2 {
		t.Fatalf("weekly quota lost: %#v", data.Buckets)
	}
	finances := data.UsageDetails.Finances
	if finances == nil || len(finances.Items) != 2 {
		t.Fatalf("missing finances: %#v", finances)
	}
	prepaid, extra := finances.Items[0], finances.Items[1]
	if prepaid.PrepaidRemainingMinor == nil || *prepaid.PrepaidRemainingMinor != 1250 || extra.ExtraUsedMinor == nil || *extra.ExtraUsedMinor != 25 || extra.ExtraCapMinor == nil || *extra.ExtraCapMinor != 100 || extra.Currency != "USD" {
		t.Fatal("string wrappers lost exact money")
	}
}

func TestBillingPreservesFractionalLegacyCreditsWithoutInventingMoney(t *testing.T) {
	for _, scalar := range []string{`1.25`, `"1.25"`, `1.25e0`, `"1.25e0"`} {
		t.Run(scalar, func(t *testing.T) {
			// Given fractional provider credits, not monetary integers.
			body := `{"config":{"used":{"val":` + scalar + `},"monthlyLimit":{"val":2.5},"onDemandUsed":{"val":` + scalar + `},"onDemandCap":{"val":2.5},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`
			data := baseUsageData()
			// When mapped through the legacy path.
			err := mapBilling(strings.NewReader(body), &data, time.Unix(1, 0))
			// Then fractional quota arithmetic is unchanged, with no rounded money.
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Buckets) != 2 || data.Buckets[0].ID != usage.BucketCredits || data.Buckets[0].Utilization != 50 || data.Buckets[1].Utilization != 50 || data.UsageDetails.Finances != nil {
				t.Fatalf("fractional credits changed: %#v", data)
			}
		})
	}
}

func TestBillingKeepsWeeklyQuotaWhenOptionalWrappersAreInvalid(t *testing.T) {
	for _, wrapper := range []string{
		`{"val":"nonnumeric"}`, `{"val":"NaN"}`, `{"val":"Infinity"}`,
		`{"val":1e309}`, `{"val":"1e309"}`, `{"val":9007199254740992}`,
		`{"val":"9007199254740992"}`, `{"val":"9223372036854775808"}`,
		`{"val":"` + strings.Repeat("1", 65) + `"}`, `{"val":null}`,
		`{"val":true}`, `{"val":[]}`, `{"val":{}}`, `[]`, `"wrong shape"`, `null`,
	} {
		t.Run(wrapper, func(t *testing.T) {
			// Given invalid optional scalars but valid weekly usage.
			body := `{"config":{"creditUsagePercent":63.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-08-01T00:00:00Z"},"prepaidBalance":` + wrapper + `,"onDemandUsed":` + wrapper + `,"onDemandCap":` + wrapper + `}}`
			data := baseUsageData()
			// When mapped at the primary boundary.
			err := mapBilling(strings.NewReader(body), &data, time.Unix(1, 0))
			// Then unusable metadata cannot fail the quota or fabricate a balance.
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Buckets) != 1 || data.Buckets[0].ID != usage.BucketWeekly || data.Current.Utilization != 63.5 || data.UsageDetails.Finances != nil || data.Buckets[0].DetailText != nil {
				t.Fatalf("invalid wrapper affected usage: %#v", data)
			}
			if _, err := json.Marshal(data); err != nil {
				t.Fatalf("nonfinite output: %v", err)
			}
		})
	}
}

func TestBillingMoneyUsesOriginalExactIntegerNotRoundedFloat(t *testing.T) {
	maximum, zero := int64(usage.MaxSafeInteger), int64(0)
	for _, tc := range []struct {
		scalar string
		want   *int64
	}{
		{`9007199254740991`, &maximum},
		{`"9007199254740991"`, &maximum},
		{`0`, &zero},
		{`"0"`, &zero},
		{`1.00000000000000001`, nil},
		{`"9007199254740991.1"`, nil},
		{`-1`, nil},
	} {
		t.Run(tc.scalar, func(t *testing.T) {
			// Given values that distinguish exact minor units from float rounding.
			body := `{"onDemandEnabled":true,"config":{"creditUsagePercent":10,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-08-01T00:00:00Z"},"onDemandCap":{"val":` + tc.scalar + `}}}`
			data := baseUsageData()
			// When mapped into financial metadata.
			err := mapBilling(strings.NewReader(body), &data, time.Unix(1, 0))
			// Then only exact original integers become money.
			if err != nil {
				t.Fatal(err)
			}
			got := data.UsageDetails.Finances.Items[0].ExtraCapMinor
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("exact money = %v, want %v", got, tc.want)
			}
		})
	}
}
