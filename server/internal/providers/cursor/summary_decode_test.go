package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSummaryMalformedOptionalMetadataPreservesCoreUsage(t *testing.T) {
	for _, mode := range []string{"classic", "weekly"} {
		for _, field := range []string{
			`"planName":{}`,
			`"billingBrand":[]`,
			`"onDemandSettings":{"enabled":"unexpected","autoTopUpEnabled":false}`,
			`"onDemandSettings":{"enabled":true,"autoTopUpEnabled":[]}`,
			`"onDemandSettings":[]`,
		} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				// Given
				fixture := modernFixture{summary: strings.TrimSuffix(classicSummaryFixture, "}") + "," + field + "}"}
				wantAuto, wantCurrent, wantCount := 12.0, 30.0, 2
				if mode == "weekly" {
					fixture.period = weeklyPeriodFixture
					wantAuto, wantCurrent, wantCount = 37, 37, 1
				}
				c := modernClient(t, fixture)
				secret := c.secret.get()
				// When
				got, err := c.Fetch(context.Background())
				// Then
				if err != nil || got.Error != nil || got.FetchFailure != nil || got.NeedsReauth || got.Auth.State != "signed_in" || c.secret.get() != secret {
					t.Fatal("optional summary metadata erased core usage or changed authentication")
				}
				auto := bucketByID(got.Buckets, "auto")
				if len(got.Buckets) != wantCount || auto == nil || auto.Utilization != wantAuto || auto.Measurement != "percent" || got.Current.Utilization != wantCurrent {
					t.Fatal("core quota changed")
				}
				if mode == "classic" {
					if api := bucketByID(got.Buckets, "api"); api == nil || api.Utilization != 48 {
						t.Fatal("classic API quota changed")
					}
				} else if auto.StartsAt == nil || auto.StartsAt.UnixMilli() != 1792972800000 || auto.ResetsAt == nil || auto.ResetsAt.UnixMilli() != 1793577600000 {
					t.Fatal("authoritative weekly period lost")
				}
			})
		}
	}
}

func TestSummaryOptionalMetadataDecodesIndependently(t *testing.T) {
	for _, tt := range []struct {
		name, body, plan, brand string
		settings                bool
	}{
		{"valid", `{"planName":"Synthetic Pro","billingBrand":"unified","onDemandSettings":{"enabled":true,"autoTopUpEnabled":false}}`, "Synthetic Pro", "unified", true},
		{"bad plan", `{"planName":{},"billingBrand":"unified","onDemandSettings":{"enabled":true,"autoTopUpEnabled":false}}`, "", "unified", true},
		{"bad brand", `{"planName":"Synthetic Pro","billingBrand":[],"onDemandSettings":{"enabled":true,"autoTopUpEnabled":false}}`, "Synthetic Pro", "", true},
		{"bad settings", `{"planName":"Synthetic Pro","billingBrand":"unified","onDemandSettings":{"enabled":true,"autoTopUpEnabled":"bad"}}`, "Synthetic Pro", "unified", false},
		{"missing", `{}`, "", "", false},
		{"null", `{"planName":null,"billingBrand":null,"onDemandSettings":null}`, "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var got cursorUsageSummary
			// When
			err := json.Unmarshal([]byte(tt.body), &got)
			// Then
			if err != nil || got.PlanName != tt.plan || got.BillingBrand != tt.brand {
				t.Fatalf("independent metadata decode failed: %v", err)
			}
			if !tt.settings {
				if got.OnDemandSettings != nil {
					t.Fatal("unknown settings became reported flags")
				}
				return
			}
			settings := got.OnDemandSettings
			if settings == nil || settings.Enabled == nil || !*settings.Enabled || settings.AutoTopUpEnabled == nil || *settings.AutoTopUpEnabled {
				t.Fatal("valid explicit settings changed")
			}
		})
	}
}

func TestSummaryMissingSettingsFlagsRemainUnknown(t *testing.T) {
	for _, body := range []string{`{"onDemandSettings":{}}`, `{"onDemandSettings":{"enabled":null,"autoTopUpEnabled":null}}`} {
		// Given
		var got cursorUsageSummary
		// When
		err := json.Unmarshal([]byte(body), &got)
		// Then
		if err != nil || got.OnDemandSettings == nil || got.OnDemandSettings.Enabled != nil || got.OnDemandSettings.AutoTopUpEnabled != nil {
			t.Fatal("missing/null settings flags became false")
		}
	}
}

func TestSummaryMalformedCoreRemainsStrict(t *testing.T) {
	for _, body := range []string{
		`{"membershipType":{},"planName":{}}`,
		`{"individualUsage":{"plan":{"totalPercentUsed":"bad"}},"billingBrand":[]}`,
		`{"individualUsage":{"plan":{"used":[]}}}`,
		`{"individualUsage":{"onDemand":{"enabled":"bad"}}}`,
		`{"teamUsage":{"pooled":{"limit":{}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			// Given
			c := modernClient(t, modernFixture{summary: body, period: weeklyPeriodFixture})
			// When
			_, _, err := c.fetchUsageSummary(context.Background(), c.secret.get())
			// Then
			var typeError *json.UnmarshalTypeError
			if !errors.As(err, &typeError) {
				t.Fatalf("malformed core summary accepted: %v", err)
			}
		})
	}
}
