package grok

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestBillingDetailsKeepCreditsSeparateFromMoney(t *testing.T) {
	data := baseUsageData()
	body := `{"config":{"used":{"val":25},"monthlyLimit":{"val":100},"billingPeriodEnd":"2026-08-01T00:00:00Z","prepaidBalance":{"val":"1250"},"onDemandCap":{"val":300},"productUsage":[{"product":"Build","usagePercent":0},{"product":"Unknown"},{"product":"bad/url","usagePercent":1},{"product":"Future_Product","usagePercent":5}]}}`
	err := mapBilling(strings.NewReader(body), &data, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	finances := data.UsageDetails.Finances.Items
	if len(finances) != 2 || *finances[0].PrepaidRemainingMinor != 1250 || *finances[1].ExtraCapMinor != 300 {
		t.Fatalf("money: %#v", finances)
	}
	for _, item := range finances {
		if item.IncludedUsedMinor != nil || item.IncludedLimitMinor != nil || item.AutoTopUpEnabled != nil {
			t.Fatal("invented credit dollars or auto-top-up")
		}
	}
	products := data.UsageDetails.ProductUsage
	if products.ParentBucketID != nil || len(products.Items) != 2 || products.Items[0].ReportedPercent != 0 || products.Items[1].Product != "Future_Product" {
		t.Fatalf("products: %#v", products)
	}
	if len(data.Buckets) != 2 || data.Buckets[0].ID != usage.BucketCredits || data.Buckets[1].Measurement != usage.MeasurementStatus {
		t.Fatalf("meters: %#v", data.Buckets)
	}
}

func TestMoneyRejectsFractionalAndUnsafeValues(t *testing.T) {
	for _, raw := range []string{`{"val":1.5}`, `{"val":"9007199254740992"}`, `{"val":null}`, `{"val":-1}`, `{"val":"1e99999999"}`} {
		var value minorValue
		err := json.Unmarshal([]byte(raw), &value)
		if err == nil && value.minor() != nil {
			t.Fatalf("accepted nonexact money %s", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"val":0}`, `{"val":"0"}`} {
		var value minorValue
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		if got := value.minor(); got == nil || *got != 0 {
			t.Fatalf("lost zero: %s", raw)
		}
	}
}

func TestMonetaryLedgerUsesOwnPeriodAndNeverHistory(t *testing.T) {
	body := `{"config":{"used":{"val":120},"monthlyLimit":{"val":"5000"},"onDemandCap":{"val":300},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"},"history":[{"onDemandUsed":{"val":12345},"includedUsed":{"val":12345}}]}`
	details, err := decodeMonetary([]byte(body), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	item := details.Finances.Items[0]
	if *item.IncludedUsedMinor != 120 || *item.IncludedLimitMinor != 5000 || *item.ExtraCapMinor != 300 || item.ExtraUsedMinor != nil || item.StartsAt.Month() != time.July || item.ResetsAt.Month() != time.August || item.Scope.Kind != "unknown" {
		t.Fatalf("ledger: %#v", item)
	}
}

func TestTopUpRequiresActualEnabledBoolean(t *testing.T) {
	for _, body := range []string{`{}`, `{"rule":{}}`, `{"rule":{"maxAmountPerMonth":{"val":1000}}}`} {
		if details, err := decodeTopUp([]byte(body), time.Unix(1, 0)); err == nil || details != nil {
			t.Fatal("invented enabled state")
		}
	}
	details, err := decodeTopUp([]byte(`{"rule":{"enabled":false,"maxAmountPerMonth":{"val":1000}}}`), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	item := details.Finances.Items[0]
	if item.AutoTopUpEnabled == nil || *item.AutoTopUpEnabled || item.ExtraCapMinor != nil {
		t.Fatal("confused auto-top-up with additional spend cap")
	}
}

func TestUserScopeExportsNoIdentity(t *testing.T) {
	for _, tc := range []struct{ body, scope string }{
		{`{"userId":"secret-user","principalType":"Team","principalId":"secret-team","teamId":"secret-team","subscriptionTier":"SuperGrok","email":"private@example.test"}`, "team"},
		{`{"userId":"secret-user","principalType":"User","principalId":"secret-user"}`, "personal"},
		{`{"userId":"secret-user","teamId":"secret-team"}`, "unknown"},
		{`{"userId":"secret-user","principalType":"Team","teamId":"secret-team","principalId":"other"}`, "unknown"},
	} {
		details, err := decodeUser([]byte(tc.body), time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if details.Plan.Scope.Kind != tc.scope || details.Plan.Scope.SharedPool != nil {
			t.Fatalf("scope: %#v", details.Plan.Scope)
		}
		encoded, err := json.Marshal(details)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"secret-user", "secret-team", "private@example.test"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("identity exported")
			}
		}
	}
}
