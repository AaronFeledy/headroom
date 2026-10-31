package usage_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestUsageDetailsCloneDetachesEveryNestedPointerAndSlice(t *testing.T) {
	// Given
	source := populatedDetails()
	// When
	cloned := source.Clone()
	// Then
	if !reflect.DeepEqual(source, cloned) {
		t.Fatal("clone changed detail values")
	}
	assertDetailsDetached(t, reflect.ValueOf(source), reflect.ValueOf(cloned))
}

func TestUsageDetailsClonePreservesNilAndEmptyCollections(t *testing.T) {
	// Given
	tests := []struct {
		name    string
		details *usage.UsageDetails
	}{
		{"nil receiver", nil},
		{"absent sections", &usage.UsageDetails{}},
		{"unknown collections", &usage.UsageDetails{
			Plan: &usage.PlanDetails{}, Quotas: []usage.QuotaDetails{{}},
			Finances: &usage.FinancialDetails{}, CreditGrants: &usage.CreditGrantDetails{},
			ResetInventory: &usage.ResetInventoryDetails{}, ProductUsage: &usage.ProductUsageDetails{},
		}},
		{"known empty collections", &usage.UsageDetails{
			Quotas:         []usage.QuotaDetails{},
			Finances:       &usage.FinancialDetails{Items: []usage.FinancialItem{}},
			CreditGrants:   &usage.CreditGrantDetails{Items: []usage.CreditGrant{}},
			ResetInventory: &usage.ResetInventoryDetails{Windows: []usage.ResetWindow{}},
			ProductUsage:   &usage.ProductUsageDetails{Items: []usage.ProductUsageItem{}},
		}},
		{"absent row fields", &usage.UsageDetails{
			Plan:           &usage.PlanDetails{Scope: &usage.UsageScope{}},
			Finances:       &usage.FinancialDetails{Items: []usage.FinancialItem{{}}},
			CreditGrants:   &usage.CreditGrantDetails{Items: []usage.CreditGrant{{}}},
			ResetInventory: &usage.ResetInventoryDetails{Windows: []usage.ResetWindow{{}}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			cloned := tt.details.Clone()
			// Then
			if !reflect.DeepEqual(tt.details, cloned) {
				t.Fatal("clone changed nil/empty semantics")
			}
			assertDetailsDetached(t, reflect.ValueOf(tt.details), reflect.ValueOf(cloned))
		})
	}
}

func assertDetailsDetached(t *testing.T, source, cloned reflect.Value) {
	t.Helper()
	switch source.Kind() {
	case reflect.Pointer:
		if source.IsNil() {
			return
		}
		if source.Pointer() == cloned.Pointer() {
			t.Fatalf("clone aliases %s pointer", source.Type())
		}
		assertDetailsDetached(t, source.Elem(), cloned.Elem())
	case reflect.Slice:
		if source.Len() > 0 && source.Pointer() == cloned.Pointer() {
			t.Fatalf("clone aliases %s backing array", source.Type())
		}
		for i := 0; i < source.Len(); i++ {
			assertDetailsDetached(t, source.Index(i), cloned.Index(i))
		}
	case reflect.Struct:
		if source.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		for i := 0; i < source.NumField(); i++ {
			assertDetailsDetached(t, source.Field(i), cloned.Field(i))
		}
	case reflect.String, reflect.Bool, reflect.Int64, reflect.Float64:
	default:
		t.Fatalf("unhandled detail field type %s", source.Type())
	}
}

func populatedDetails() *usage.UsageDetails {
	observed := time.Date(2026, 10, 29, 12, 0, 0, 123456789, time.UTC)
	starts, resets := observed.Add(-time.Hour), observed.Add(time.Hour)
	expires, validFrom := observed.Add(24*time.Hour), observed.Add(-time.Minute)
	observation := usage.Observation{State: usage.DetailOK, ObservedAt: &observed}
	label, product, bucket := "Pro", "Grok Code", "plan"
	zero, limit, remaining, prepaid, extra, cap := int64(0), usage.MaxSafeInteger, int64(2400), int64(1200), int64(150), int64(5000)
	reported, valid := int64(3), int64(2)
	enabled, disabled := true, false
	scope := &usage.UsageScope{Kind: "team", SharedPool: &enabled}
	return &usage.UsageDetails{
		Plan:   &usage.PlanDetails{Observation: observation, Label: &label, IsUnlimited: &disabled, Scope: scope},
		Quotas: []usage.QuotaDetails{{Observation: observation, BucketID: bucket, PlanLabel: &label, HasAvailableUsage: &enabled, Scope: scope}},
		Finances: &usage.FinancialDetails{Observation: observation, Items: []usage.FinancialItem{{
			Currency: "USD", Scope: scope, StartsAt: &starts, ResetsAt: &resets,
			IncludedUsedMinor: &zero, IncludedLimitMinor: &limit, IncludedRemainingMinor: &remaining,
			PrepaidRemainingMinor: &prepaid, ExtraUsedMinor: &extra, ExtraCapMinor: &cap,
			ExtraEnabled: &enabled, AutoTopUpEnabled: &disabled,
		}}},
		CreditGrants: &usage.CreditGrantDetails{Observation: observation, Items: []usage.CreditGrant{{
			DisplayName: "Welcome bonus", Product: &product, Currency: "USD", RemainingMinor: &remaining,
			TotalMinor: &limit, ExpiresAt: &expires, Scope: scope,
		}}},
		ResetInventory: &usage.ResetInventoryDetails{
			Observation: observation, ReportedCount: &reported, ValidNowCount: &valid,
			Windows: []usage.ResetWindow{{Count: 2, ValidFrom: &validFrom, ExpiresAt: &expires}},
		},
		ProductUsage: &usage.ProductUsageDetails{
			Observation: observation, ParentBucketID: &bucket,
			Items: []usage.ProductUsageItem{{Product: product, ReportedPercent: 12.5}},
		},
	}
}
