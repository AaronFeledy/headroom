package usage

import (
	"slices"
	"time"
)

// Clone returns an independent copy of every nested pointer and slice, preserving
// nil versus empty collections. A nil receiver returns nil.
func (d *UsageDetails) Clone() *UsageDetails {
	if d == nil {
		return nil
	}
	cloned := *d
	if d.Plan != nil {
		plan := *d.Plan
		plan.ObservedAt = cloneDetailValue(d.Plan.ObservedAt)
		plan.Label = cloneDetailValue(d.Plan.Label)
		plan.IsUnlimited = cloneDetailValue(d.Plan.IsUnlimited)
		plan.Scope = cloneDetailScope(d.Plan.Scope)
		cloned.Plan = &plan
	}
	cloned.Quotas = slices.Clone(d.Quotas)
	for i, quota := range d.Quotas {
		cloned.Quotas[i].ObservedAt = cloneDetailValue(quota.ObservedAt)
		cloned.Quotas[i].PlanLabel = cloneDetailValue(quota.PlanLabel)
		cloned.Quotas[i].HasAvailableUsage = cloneDetailValue(quota.HasAvailableUsage)
		cloned.Quotas[i].Scope = cloneDetailScope(quota.Scope)
	}
	if d.Finances != nil {
		finances := *d.Finances
		finances.ObservedAt = cloneDetailValue(d.Finances.ObservedAt)
		finances.Items = slices.Clone(d.Finances.Items)
		for i, item := range d.Finances.Items {
			finances.Items[i].Scope = cloneDetailScope(item.Scope)
			finances.Items[i].StartsAt = cloneDetailValue(item.StartsAt)
			finances.Items[i].ResetsAt = cloneDetailValue(item.ResetsAt)
			finances.Items[i].IncludedUsedMinor = cloneDetailValue(item.IncludedUsedMinor)
			finances.Items[i].IncludedLimitMinor = cloneDetailValue(item.IncludedLimitMinor)
			finances.Items[i].IncludedRemainingMinor = cloneDetailValue(item.IncludedRemainingMinor)
			finances.Items[i].PrepaidRemainingMinor = cloneDetailValue(item.PrepaidRemainingMinor)
			finances.Items[i].ExtraUsedMinor = cloneDetailValue(item.ExtraUsedMinor)
			finances.Items[i].ExtraCapMinor = cloneDetailValue(item.ExtraCapMinor)
			finances.Items[i].ExtraEnabled = cloneDetailValue(item.ExtraEnabled)
			finances.Items[i].AutoTopUpEnabled = cloneDetailValue(item.AutoTopUpEnabled)
		}
		cloned.Finances = &finances
	}
	if d.CreditGrants != nil {
		grants := *d.CreditGrants
		grants.ObservedAt = cloneDetailValue(d.CreditGrants.ObservedAt)
		grants.Items = slices.Clone(d.CreditGrants.Items)
		for i, item := range d.CreditGrants.Items {
			grants.Items[i].Product = cloneDetailValue(item.Product)
			grants.Items[i].RemainingMinor = cloneDetailValue(item.RemainingMinor)
			grants.Items[i].TotalMinor = cloneDetailValue(item.TotalMinor)
			grants.Items[i].ExpiresAt = cloneDetailValue(item.ExpiresAt)
			grants.Items[i].Scope = cloneDetailScope(item.Scope)
		}
		cloned.CreditGrants = &grants
	}
	if d.ResetInventory != nil {
		inventory := *d.ResetInventory
		inventory.ObservedAt = cloneDetailValue(d.ResetInventory.ObservedAt)
		inventory.ReportedCount = cloneDetailValue(d.ResetInventory.ReportedCount)
		inventory.ValidNowCount = cloneDetailValue(d.ResetInventory.ValidNowCount)
		inventory.Windows = slices.Clone(d.ResetInventory.Windows)
		for i, window := range d.ResetInventory.Windows {
			inventory.Windows[i].ValidFrom = cloneDetailValue(window.ValidFrom)
			inventory.Windows[i].ExpiresAt = cloneDetailValue(window.ExpiresAt)
		}
		cloned.ResetInventory = &inventory
	}
	if d.ProductUsage != nil {
		products := *d.ProductUsage
		products.ObservedAt = cloneDetailValue(d.ProductUsage.ObservedAt)
		products.ParentBucketID = cloneDetailValue(d.ProductUsage.ParentBucketID)
		products.Items = slices.Clone(d.ProductUsage.Items)
		cloned.ProductUsage = &products
	}
	return &cloned
}

func cloneDetailScope(scope *UsageScope) *UsageScope {
	if scope == nil {
		return nil
	}
	return &UsageScope{Kind: scope.Kind, SharedPool: cloneDetailValue(scope.SharedPool)}
}

func cloneDetailValue[T string | bool | int64 | time.Time](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
