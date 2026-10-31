package grok

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const resetInventoryURL = "https://grok.com/prod_mc_billing.ConsumerUiSvc/GetRemainingResets"

type extraEntry struct {
	details *usage.UsageDetails
	next    time.Time
	failed  bool
}

type extraCache struct {
	mu      sync.Mutex
	key     string
	entries [4]extraEntry
}

func optionalURLs(opts Options) [4]string {
	defaults := [4]string{billingURL, "https://cli-chat-proxy.grok.com/v1/auto-topup-rule", "https://cli-chat-proxy.grok.com/v1/user?include=subscription", resetInventoryURL}
	// An injected billing origin must never silently send its credentials to
	// production enrichment endpoints, even when individual overrides are absent.
	if opts.BillingURL != "" {
		if base, err := url.Parse(opts.BillingURL); err == nil {
			for i, path := range []string{"/v1/billing", "/v1/auto-topup-rule", "/v1/user?include=subscription", "/prod_mc_billing.ConsumerUiSvc/GetRemainingResets"} {
				ref, err := url.Parse(path)
				if err == nil {
					defaults[i] = base.ResolveReference(ref).String()
				}
			}
		}
	}
	for i, override := range []string{opts.PlainBillingURL, opts.AutoTopUpURL, opts.UserURL, opts.ResetInventoryURL} {
		if override != "" {
			defaults[i] = override
		}
	}
	return defaults
}

func hasWeekly(data usage.UsageData) bool {
	for _, bucket := range data.Buckets {
		if bucket.ID == usage.BucketWeekly {
			return true
		}
	}
	return false
}

func (p *Provider) populateExtras(ctx context.Context, creds credentials, data *usage.UsageData) {
	cache := &p.extras
	if !cache.mu.TryLock() {
		return
	}
	defer cache.mu.Unlock()
	key := ""
	if data.CredentialEpoch != nil {
		key = *data.CredentialEpoch
	}
	for _, bucket := range data.Buckets {
		if bucket.StartsAt != nil {
			key += "|" + bucket.ID + bucket.StartsAt.UTC().Format(time.RFC3339Nano)
		}
		if bucket.ResetsAt != nil {
			key += "|" + bucket.ID + bucket.ResetsAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if key != cache.key {
		cache.key, cache.entries = key, [4]extraEntry{}
	}
	groupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	jobs := make(chan int, 4)
	now := p.now()
	for i, entry := range cache.entries {
		if !now.Before(entry.next) {
			jobs <- i
		}
	}
	close(jobs)
	for range 2 {
		wg.Go(func() {
			for i := range jobs {
				child, stop := context.WithTimeout(groupCtx, 2*time.Second)
				details, retry := p.readExtra(child, i, creds)
				stop()
				entry := &cache.entries[i]
				entry.next, entry.failed = now.Add(5*time.Minute), details == nil
				if retry.After(entry.next) {
					entry.next = retry
				}
				if details != nil {
					entry.details = details.Clone()
				}
			}
		})
	}
	wg.Wait()
	if data.UsageDetails == nil {
		data.UsageDetails = &usage.UsageDetails{}
	}
	for _, entry := range cache.entries {
		details := entry.details.Clone()
		if details == nil {
			continue
		}
		mergeExtras(data.UsageDetails, details, entry.failed, now)
	}
	if data.UsageDetails.ResetInventory == nil {
		data.UsageDetails.ResetInventory = &usage.ResetInventoryDetails{Observation: usage.Observation{State: usage.DetailUnknown}}
	}
	if data.UsageDetails.Plan == nil && data.Subtitle != nil {
		if label := safeProduct(*data.Subtitle); label != "" {
			data.UsageDetails.Plan = &usage.PlanDetails{Observation: observation(now), Label: &label}
		}
	}
}

func mergeExtras(target, source *usage.UsageDetails, failed bool, now time.Time) {
	valid := func(o *usage.Observation) bool {
		if o.ObservedAt == nil || now.Sub(*o.ObservedAt) > 15*time.Minute {
			return false
		}
		if failed {
			o.State = usage.DetailStale
		}
		return true
	}
	if source.Plan != nil && valid(&source.Plan.Observation) {
		target.Plan = source.Plan
	}
	if source.ResetInventory != nil && valid(&source.ResetInventory.Observation) {
		count := int64(0)
		complete := source.ResetInventory.State == usage.DetailOK && source.ResetInventory.Windows != nil
		for _, window := range source.ResetInventory.Windows {
			if window.ValidFrom == nil || window.ExpiresAt == nil {
				complete = false
			} else if !now.Before(*window.ValidFrom) && now.Before(*window.ExpiresAt) {
				count += window.Count
			}
		}
		source.ResetInventory.ValidNowCount = nil
		if complete {
			source.ResetInventory.ValidNowCount = &count
		}
		target.ResetInventory = source.ResetInventory
	}
	if source.Finances != nil && valid(&source.Finances.Observation) {
		if target.Finances == nil {
			target.Finances = source.Finances
			return
		}
		target.Finances.Items = append(target.Finances.Items, source.Finances.Items...)
		if source.Finances.ObservedAt.Before(*target.Finances.ObservedAt) {
			target.Finances.ObservedAt = source.Finances.ObservedAt
		}
		if source.Finances.State == usage.DetailStale {
			target.Finances.State = usage.DetailStale
		}
	}
}
